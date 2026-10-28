package server

type queuedCmd struct {
	cmd  *Command
	args [][]byte
}

type multiState struct {
	cmds  []queuedCmd
	dirty bool
}

func cmdMulti(c *Ctx) {
	cl := c.cl
	if cl.multi != nil {
		c.Err("ERR MULTI calls can not be nested")
		return
	}
	cl.multi = &multiState{}
	cl.multiLen.Store(0)
	c.W.WriteOK()
}

func cmdDiscard(c *Ctx) {
	cl := c.cl
	if cl.multi == nil {
		c.Err("ERR DISCARD without MULTI")
		return
	}
	cl.multi = nil
	cl.multiLen.Store(-1)
	c.s.watches.unwatchAll(cl)
	c.W.WriteOK()
}

func cmdWatch(c *Ctx) {
	cl := c.cl
	if cl.multi != nil {
		c.Err("ERR WATCH inside MULTI is not allowed")
		return
	}
	for _, k := range c.Args[1:] {
		c.s.watches.watch(cl, cl.db, string(k))
	}
	c.W.WriteOK()
}

func cmdUnwatch(c *Ctx) {
	c.s.watches.unwatchAll(c.cl)
	c.W.WriteOK()
}

// cmdExec runs the queued commands atomically: it locks the union of all
// shards touched by the queued commands and the WATCHed keys, re-checks the
// optimistic locks under those shard locks, then executes everything.
func cmdExec(c *Ctx) {
	s := c.s
	cl := c.cl
	if cl.multi == nil {
		c.Err("ERR EXEC without MULTI")
		return
	}
	m := cl.multi
	cl.multi = nil
	cl.multiLen.Store(-1)
	defer s.watches.unwatchAll(cl)
	if m.dirty {
		c.Err("EXECABORT Transaction discarded because of previous errors.")
		return
	}
	if cl.casDirty.Load() {
		c.W.WriteNullArray()
		return
	}
	var keys []string
	lockAll := false
	write := false
	for _, q := range m.cmds {
		ks, all := keysOf(q.cmd, q.args)
		if all || q.cmd.Has(FlagGlobal) {
			lockAll = true
		}
		if q.cmd.Has(FlagWrite) {
			write = true
		}
		keys = append(keys, ks...)
	}
	for _, wk := range cl.watched {
		keys = append(keys, wk.key)
	}
	barrier := lockAll && write
	if barrier {
		s.ks.HoldSnapshots()
		defer s.ks.ReleaseSnapshots()
	}
	var idx []int
	if lockAll {
		s.ks.LockAll(write)
		defer s.ks.UnlockAll(write)
	} else if len(keys) > 0 {
		idx = s.ks.ShardSet(keys, nil)
		s.ks.Lock(idx, write)
		defer s.ks.Unlock(idx, write)
	}
	if cl.casDirty.Load() {
		c.W.WriteNullArray()
		return
	}
	unit := &execUnit{db: -1}
	c.W.WriteArrayLen(len(m.cmds))
	for _, q := range m.cmds {
		if q.cmd.Has(FlagWrite) && s.repl.isReplica() && s.cfg.ReplicaReadOnly.Load() && !cl.isMaster {
			c.W.WriteError(errReadonly)
			continue
		}
		s.feedMonitors(cl, q.args)
		s.call(cl, q.cmd, q.args, c.W, unit, true, false)
	}
	if len(unit.cmds) > 0 {
		s.prop.propagate(unit.cmds, true)
	}
}
