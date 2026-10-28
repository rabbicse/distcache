package server

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/storage"
)

func (s *Server) lookupCommand(name []byte) *Command {
	var buf [32]byte
	if len(name) <= len(buf) {
		b := buf[:len(name)]
		for i, ch := range name {
			if ch >= 'A' && ch <= 'Z' {
				ch += 32
			}
			b[i] = ch
		}
		return s.cmds[string(b)]
	}
	return s.cmds[strings.ToLower(string(name))]
}

// pubsub context (RESP2) allows only these commands.
func allowedInPubSub(name string) bool {
	switch name {
	case "subscribe", "unsubscribe", "psubscribe", "punsubscribe", "ssubscribe", "sunsubscribe",
		"ping", "quit", "reset":
		return true
	}
	return false
}

func argsPreview(args [][]byte) string {
	var b strings.Builder
	for i, a := range args {
		if i >= 128 {
			break
		}
		b.WriteString("'")
		if len(a) > 128 {
			a = a[:128]
		}
		b.Write(a)
		b.WriteString("' ")
	}
	return b.String()
}

// processCommand performs the pre-execution checks of Redis' processCommand
// and then runs (or queues) the command.
func (s *Server) processCommand(c *Client, args [][]byte) {
	w := c.w
	switch c.replyMode {
	case replyOff, replySkip:
		w = resp.NewWriter(nil, c.proto)
		if c.replyMode == replySkip {
			c.replyMode = replyOn
		}
	case replySkipNext:
		c.replyMode = replySkip
	}
	cmd := s.lookupCommand(args[0])
	if cmd == nil {
		s.stats.errorReplies.Add(1)
		w.WriteError("ERR unknown command '" + truncate(string(args[0]), 128) + "', with args beginning with: " + argsPreview(args[1:]))
		if c.multi != nil {
			c.multi.dirty = true
		}
		return
	}
	if (cmd.Arity > 0 && len(args) != cmd.Arity) || (cmd.Arity < 0 && len(args) < -cmd.Arity) {
		s.stats.errorReplies.Add(1)
		cmd.Rejected.Add(1)
		w.WriteError("ERR wrong number of arguments for '" + cmd.Name + "' command")
		if c.multi != nil {
			c.multi.dirty = true
		}
		return
	}
	if !c.authed && !cmd.Has(FlagNoAuth) {
		if !s.cfg.RequirePassSet.Load() {
			c.authed = true
		} else {
			s.stats.errorReplies.Add(1)
			cmd.Rejected.Add(1)
			w.WriteError(errNoAuth)
			if c.multi != nil {
				c.multi.dirty = true
			}
			return
		}
	}
	if c.multi != nil && !cmd.Has(FlagTxControl) {
		if cmd.Has(FlagNoMulti) {
			w.WriteError("ERR Command not allowed inside a transaction")
			c.multi.dirty = true
			return
		}
		if rejected := s.preExecRejection(c, cmd, args); rejected != "" {
			w.WriteError(rejected)
			c.multi.dirty = true
			return
		}
		cp := make([][]byte, len(args))
		copy(cp, args)
		c.multi.cmds = append(c.multi.cmds, queuedCmd{cmd: cmd, args: cp})
		c.multiLen.Store(int32(len(c.multi.cmds)))
		w.WriteSimple("QUEUED")
		return
	}
	if c.proto < 3 && c.inPubSub() && !allowedInPubSub(cmd.Name) {
		w.WriteError("ERR Can't execute '" + cmd.Name + "': only (P|S)SUBSCRIBE / (P|S)UNSUBSCRIBE / PING / QUIT / RESET are allowed in this context")
		return
	}
	if rejected := s.preExecRejection(c, cmd, args); rejected != "" {
		s.stats.errorReplies.Add(1)
		cmd.Rejected.Add(1)
		w.WriteError(rejected)
		return
	}
	s.waitPause(cmd)
	s.feedMonitors(c, args)
	s.call(c, cmd, args, w, nil, false, false)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// preExecRejection returns an error message when the command can't run now.
func (s *Server) preExecRejection(c *Client, cmd *Command, args [][]byte) string {
	if s.loading.Load() && !cmd.Has(FlagLoading) && !c.isMaster {
		return "LOADING DistCache is loading the dataset in memory"
	}
	if s.scripts.busy() && !cmd.Has(FlagAllowBusy) && !c.isMaster {
		switch cmd.Name {
		case "script", "function", "shutdown":
		default:
			return "BUSY Redis is busy running a script. You can only call SCRIPT KILL or SHUTDOWN NOSAVE."
		}
	}
	if cmd.Has(FlagWrite) && s.repl.isReplica() && s.cfg.ReplicaReadOnly.Load() && !c.isMaster {
		return errReadonly
	}
	if cmd.Has(FlagDenyOOM) && s.cfg.MaxMemory.Load() > 0 && !c.isMaster {
		if !s.performEvictions() {
			return errOOM
		}
	}
	if cmd.Has(FlagWrite) && s.cfg.StopWritesOnBgsave.Load() && !s.stats.lastBgsaveOK.Load() &&
		len(s.cfg.Save) > 0 && !c.isMaster {
		return "MISCONF Errors writing to disk; write commands are disabled. Check logs for details."
	}
	return ""
}

func (s *Server) waitPause(cmd *Command) {
	end := s.pauseEnd.Load()
	if end == 0 {
		return
	}
	if !s.pauseAll.Load() && !cmd.Has(FlagWrite) && !cmd.Has(FlagMayReplicate) {
		return
	}
	for {
		end = s.pauseEnd.Load()
		d := time.Until(time.UnixMilli(end))
		if end == 0 || d <= 0 {
			return
		}
		if d > 50*time.Millisecond {
			d = 50 * time.Millisecond
		}
		time.Sleep(d)
	}
}

// keysOf extracts key names; lockAll reports whether the command needs the
// whole keyspace locked.
func keysOf(cmd *Command, args [][]byte) ([]string, bool) {
	pos, lockAll := cmd.KeyPositions(args)
	if cmd.Has(FlagLockAll) {
		lockAll = true
	}
	keys := make([]string, 0, len(pos))
	for _, p := range pos {
		if p < len(args) {
			keys = append(keys, string(args[p]))
		}
	}
	return keys, lockAll
}

// call executes a command. When locked is true the caller (EXEC, scripts)
// already holds the needed shard locks and an execution unit.
func (s *Server) call(c *Client, cmd *Command, args [][]byte, w *resp.Writer, unit *execUnit, locked, inScript bool) *Ctx {
	start := time.Now()
	keys, lockAll := keysOf(cmd, args)
	ctx := &Ctx{s: s, cl: c, Cmd: cmd, Args: args, W: w, keys: keys, inScript: inScript}
	ctx.isMaster = c != nil && c.isMaster
	ctx.inMulti = locked && !inScript
	ctx.allLocks = locked
	write := cmd.Has(FlagWrite)
	ctx.DB = s.newDB(c, write)
	ctx.canBlock = cmd.Has(FlagBlocking) && !locked && c != nil

	if locked {
		ctx.unit = unit
		s.runHandler(ctx)
	} else {
		ctx.unit = &execUnit{db: -1}
		barrier := lockAll && (write || cmd.Has(FlagLockAll))
		if barrier {
			s.ks.HoldSnapshots()
		}
		var idx []int
		var shardBuf [8]int
		hasLocks := false
		if lockAll {
			s.ks.LockAll(write)
			ctx.allLocks = true
			hasLocks = true
		} else if len(keys) > 0 {
			idx = s.ks.ShardSet(keys, shardBuf[:0])
			s.ks.Lock(idx, write)
			hasLocks = true
		}
		for {
			ctx.DB.Now = time.Now().UnixMilli()
			s.runHandler(ctx)
			if !ctx.blocked {
				break
			}
			// Register while still holding the locks so no wake-up is lost.
			waiter := s.blocker.register(c, ctx.DB.Idx, ctx.blockKeys)
			s.flushUnit(ctx)
			if hasLocks {
				if lockAll {
					s.ks.UnlockAll(write)
				} else {
					s.ks.Unlock(idx, write)
				}
			}
			outcome := s.waitBlocked(c, waiter, ctx)
			s.blocker.unregister(waiter)
			if outcome != blockWoken {
				if outcome == blockUnblockErr {
					w.WriteError("UNBLOCKED client unblocked via CLIENT UNBLOCK")
				} else if outcome == blockTimeout || outcome == blockUnblockTimeout {
					ctx.timeoutReply(w)
				}
				hasLocks = false
				break
			}
			if lockAll {
				s.ks.LockAll(write)
			} else {
				s.ks.Lock(idx, write)
			}
			ctx.blocked = false
			ctx.dirty = 0
			ctx.prop = nil
			ctx.propSet = false
			ctx.unit = &execUnit{db: -1}
		}
		s.flushUnit(ctx)
		if hasLocks {
			if lockAll {
				s.ks.UnlockAll(write)
			} else {
				s.ks.Unlock(idx, write)
			}
		}
		if barrier {
			s.ks.ReleaseSnapshots()
		}
	}
	dur := time.Since(start)
	cmd.Calls.Add(1)
	cmd.Usec.Add(dur.Microseconds())
	s.stats.commands.Add(1)
	if ctx.errored {
		cmd.Failed.Add(1)
		s.stats.errorReplies.Add(1)
	}
	if c != nil {
		c.lastCmd.Store(cmd.Name)
		if !cmd.Has(FlagSkipSlowlog) && !cmd.Has(FlagBlocking) {
			s.slowlog.maybeAdd(s, c, args, dur)
		}
	}
	return ctx
}

func (s *Server) newDB(c *Client, write bool) *storage.DB {
	db := &storage.DB{KS: s.ks, Write: write, Now: time.Now().UnixMilli()}
	if c != nil {
		db.Idx = c.db
		db.NoTouch = c.noTouch
		if c.isMaster {
			db.IgnoreExpiry = true
		}
	}
	db.ReplicaMode = s.repl.isReplica()
	db.OnExpire = func(dbi int, key string) { s.onKeyExpired(dbi, key) }
	return db
}

// runHandler executes the handler and its post-processing (memory
// accounting, propagation, WATCH invalidation, wake-ups, notifications).
func (s *Server) runHandler(ctx *Ctx) {
	cmd := ctx.Cmd
	write := cmd.Has(FlagWrite)
	if write && s.ks.SnapshotActive() {
		for _, k := range ctx.keys {
			ctx.DB.Preserve(k)
		}
	}
	cmd.Handler(ctx)
	if ctx.blocked {
		return
	}
	if ctx.dirty > 0 {
		for _, k := range ctx.keys {
			ctx.DB.Updated(k)
		}
		s.stats.dirtySinceSave.Add(ctx.dirty)
		if !cmd.Has(FlagNoPropagate) && !ctx.isMaster {
			if ctx.propSet {
				for _, p := range ctx.prop {
					ctx.unit.add(ctx.DB.Idx, p)
				}
			} else {
				ctx.unit.add(ctx.DB.Idx, ctx.Args)
			}
		}
		if len(ctx.keys) > 0 {
			s.watches.touch(ctx.DB.Idx, ctx.keys)
			s.blocker.signal(ctx.DB.Idx, ctx.keys)
			if cmd.NotifyClass != 0 {
				for _, k := range ctx.keys {
					s.notify(cmd.NotifyClass, cmd.Event, k, ctx.DB.Idx)
				}
			}
		}
	} else if ctx.propSet && len(ctx.prop) > 0 && !cmd.Has(FlagNoPropagate) && !ctx.isMaster {
		for _, p := range ctx.prop {
			ctx.unit.add(ctx.DB.Idx, p)
		}
	}
	if write {
		s.stats.totalWritesProc.Add(1)
	}
}

func (u *execUnit) add(db int, args [][]byte) {
	u.cmds = append(u.cmds, propCmd{db: db, args: args})
}

// flushUnit hands the propagated commands to AOF/replicas. Must be called
// while still holding the shard locks to preserve per-key ordering.
func (s *Server) flushUnit(ctx *Ctx) {
	u := ctx.unit
	if u == nil || len(u.cmds) == 0 {
		return
	}
	s.prop.propagate(u.cmds, false)
	u.cmds = nil
}

// onKeyExpired is called for lazily and actively expired keys: it records
// stats, propagates a DEL and emits the keyspace event.
func (s *Server) onKeyExpired(db int, key string) {
	s.stats.expiredKeys.Add(1)
	s.prop.propagate([]propCmd{{db: db, args: [][]byte{[]byte("DEL"), []byte(key)}}}, false)
	s.watches.touch(db, []string{key})
	s.notify(config.NotifyExpired, "expired", key, db)
}

// ---- MONITOR ----

func (s *Server) feedMonitors(c *Client, args [][]byte) {
	if s.monitorN.Load() == 0 || c.isMaster {
		return
	}
	cmd := s.lookupCommand(args[0])
	if cmd != nil && (cmd.Has(FlagSkipMonitor) || cmd.Has(FlagAdmin)) {
		return
	}
	s.feedMonitorsRaw(c.db, c.addr, args, false)
}

func (s *Server) feedMonitorsRaw(db int, addr string, args [][]byte, lua bool) {
	if s.monitorN.Load() == 0 {
		return
	}
	now := time.Now()
	var b bytes.Buffer
	b.WriteString(strconv.FormatInt(now.Unix(), 10))
	b.WriteByte('.')
	us := strconv.FormatInt(int64(now.Nanosecond()/1000), 10)
	b.WriteString(strings.Repeat("0", 6-len(us)) + us)
	b.WriteString(" [")
	b.WriteString(strconv.Itoa(db))
	b.WriteByte(' ')
	if lua {
		b.WriteString("lua")
	} else {
		b.WriteString(addr)
	}
	b.WriteString("]")
	for _, a := range args {
		b.WriteString(" \"")
		b.WriteString(quoteMonitor(a))
		b.WriteByte('"')
	}
	line := b.String()
	s.monitorsMu.RLock()
	defer s.monitorsMu.RUnlock()
	for m := range s.monitors {
		w := resp.NewWriter(nil, m.proto)
		w.WriteSimple(line)
		m.push(append([]byte(nil), w.Bytes()...))
	}
}

func quoteMonitor(a []byte) string {
	var b strings.Builder
	for _, ch := range a {
		switch ch {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '\a':
			b.WriteString("\\a")
		case '\b':
			b.WriteString("\\b")
		default:
			if ch < 32 || ch > 126 {
				b.WriteString("\\x")
				b.WriteString(strconv.FormatInt(int64(ch)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(ch)&15, 16))
			} else {
				b.WriteByte(ch)
			}
		}
	}
	return b.String()
}

func (s *Server) removeMonitor(c *Client) {
	if !c.isMonitor {
		return
	}
	s.monitorsMu.Lock()
	if _, ok := s.monitors[c]; ok {
		delete(s.monitors, c)
		s.monitorN.Add(-1)
	}
	s.monitorsMu.Unlock()
}

// ---- iteration helpers for FlagGlobal commands ----

// forEachShard runs fn for every shard; when the context does not already
// hold all locks each shard is locked individually (non-atomic, like SCAN).
func (c *Ctx) forEachShard(write bool, fn func(i int) bool) {
	n := c.s.ks.NumShards()
	for i := 0; i < n; i++ {
		if !c.allLocks {
			c.s.ks.LockShard(i, write)
		}
		cont := fn(i)
		if !c.allLocks {
			c.s.ks.UnlockShard(i, write)
		}
		if !cont {
			return
		}
	}
}

// withShard runs fn holding the lock for shard i (if not already held).
func (c *Ctx) withShard(i int, write bool, fn func()) {
	if !c.allLocks {
		c.s.ks.LockShard(i, write)
		defer c.s.ks.UnlockShard(i, write)
	}
	fn()
}

// withKeys locks the shards of keys (if not already held) and runs fn.
func (c *Ctx) withKeys(keys []string, write bool, fn func()) {
	if c.allLocks {
		fn()
		return
	}
	idx := c.s.ks.ShardSet(keys, nil)
	c.s.ks.Lock(idx, write)
	defer c.s.ks.Unlock(idx, write)
	fn()
}
