package server

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/storage"
)

func cmdPing(c *Ctx) {
	if len(c.Args) > 2 {
		c.WrongArgs()
		return
	}
	if c.cl != nil && c.cl.inPubSub() && c.W.Proto < 3 {
		c.W.WriteArrayLen(2)
		c.W.WriteBulkString("pong")
		if len(c.Args) == 2 {
			c.W.WriteBulk(c.Args[1])
		} else {
			c.W.WriteBulkString("")
		}
		return
	}
	if len(c.Args) == 2 {
		c.W.WriteBulk(c.Args[1])
		return
	}
	c.W.WriteSimple("PONG")
}

func cmdEcho(c *Ctx) { c.W.WriteBulk(c.Args[1]) }

func cmdQuit(c *Ctx) {
	c.W.WriteOK()
	if c.cl != nil {
		c.cl.closeAfterReply = true
	}
}

func cmdReset(c *Ctx) {
	cl := c.cl
	cl.multi = nil
	cl.multiLen.Store(-1)
	c.s.watches.unwatchAll(cl)
	c.s.pubsub.unsubscribeAll(cl)
	c.s.removeMonitor(cl)
	cl.isMonitor = false
	cl.db = 0
	cl.dbAtomic.Store(0)
	cl.proto = 2
	cl.w.Proto = 2
	c.W.Proto = 2
	cl.name.Store("")
	cl.replyMode = replyOn
	cl.noEvict = false
	cl.noTouch = false
	cl.authed = !c.s.cfg.RequirePassSet.Load()
	c.W.WriteSimple("RESET")
}

func (s *Server) checkPassword(user, pass string) bool {
	if user != "default" {
		return false
	}
	rp := s.cfg.RequirePass()
	if rp == "" {
		return true
	}
	return pass == rp
}

func cmdAuth(c *Ctx) {
	if len(c.Args) > 3 {
		c.SyntaxErr()
		return
	}
	user, pass := "default", c.Arg(1)
	if len(c.Args) == 3 {
		user, pass = c.Arg(1), c.Arg(2)
	}
	if len(c.Args) == 2 && !c.s.cfg.RequirePassSet.Load() {
		c.Err("ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?")
		return
	}
	if !c.s.checkPassword(user, pass) {
		c.Err("WRONGPASS invalid username-password pair or user is disabled.")
		return
	}
	c.cl.authed = true
	c.cl.user = user
	c.W.WriteOK()
}

func cmdHello(c *Ctx) {
	cl := c.cl
	proto := cl.proto
	i := 1
	if len(c.Args) > 1 {
		n, ok := parseInt(c.Args[1])
		if !ok {
			c.Err("ERR Protocol version is not an integer or out of range")
			return
		}
		if n < 2 || n > 3 {
			c.Err("NOPROTO unsupported protocol version")
			return
		}
		proto = int(n)
		i = 2
	}
	var name string
	nameSet := false
	for ; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case c.Is(i, "auth") && left >= 2:
			if !c.s.checkPassword(c.Arg(i+1), c.Arg(i+2)) {
				c.Err("WRONGPASS invalid username-password pair or user is disabled.")
				return
			}
			cl.authed = true
			cl.user = c.Arg(i + 1)
			i += 2
		case c.Is(i, "setname") && left >= 1:
			if !validClientName(c.Args[i+1]) {
				c.Err("ERR Client names cannot contain spaces, newlines or special characters.")
				return
			}
			name = c.Arg(i + 1)
			nameSet = true
			i++
		default:
			c.Err("ERR Syntax error in HELLO option '" + c.Arg(i) + "'")
			return
		}
	}
	if !cl.authed {
		c.Err("NOAUTH HELLO must be called with the client already authenticated, otherwise the HELLO <proto> AUTH <user> <pass> option can be used to authenticate the client and select the RESP protocol version at the same time")
		return
	}
	if nameSet {
		cl.name.Store(name)
	}
	cl.proto = proto
	cl.w.Proto = proto
	c.W.Proto = proto
	role := "master"
	if c.s.repl.isReplica() {
		role = "replica"
	}
	c.W.WriteMapLen(7)
	c.W.WriteBulkString("server")
	c.W.WriteBulkString("redis")
	c.W.WriteBulkString("version")
	c.W.WriteBulkString(RedisVersion)
	c.W.WriteBulkString("proto")
	c.W.WriteInt(int64(proto))
	c.W.WriteBulkString("id")
	c.W.WriteInt(cl.id)
	c.W.WriteBulkString("mode")
	c.W.WriteBulkString("standalone")
	c.W.WriteBulkString("role")
	c.W.WriteBulkString(role)
	c.W.WriteBulkString("modules")
	c.W.WriteArrayLen(0)
}

func validClientName(b []byte) bool {
	for _, ch := range b {
		if ch < '!' || ch > '~' {
			return false
		}
	}
	return true
}

func cmdClient(c *Ctx) {
	s := c.s
	cl := c.cl
	sub := strings.ToLower(c.Arg(1))
	switch sub {
	case "id":
		c.W.WriteInt(cl.id)
	case "getname":
		n := cl.name.Load().(string)
		if n == "" {
			c.W.WriteNull()
		} else {
			c.W.WriteBulkString(n)
		}
	case "setname":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		if !validClientName(c.Args[2]) {
			c.Err("ERR Client names cannot contain spaces, newlines or special characters.")
			return
		}
		cl.name.Store(c.Arg(2))
		c.W.WriteOK()
	case "setinfo":
		if len(c.Args) != 4 {
			c.WrongSubArgs()
			return
		}
		switch {
		case c.Is(2, "lib-name"):
			if !validClientName(c.Args[3]) {
				c.Err("ERR lib-name cannot contain spaces, newlines or special characters.")
				return
			}
			cl.libName.Store(c.Arg(3))
		case c.Is(2, "lib-ver"):
			if !validClientName(c.Args[3]) {
				c.Err("ERR lib-ver cannot contain spaces, newlines or special characters.")
				return
			}
			cl.libVer.Store(c.Arg(3))
		default:
			c.Err("ERR Unrecognized option '" + c.Arg(2) + "'")
			return
		}
		c.W.WriteOK()
	case "info":
		c.W.WriteVerbatim(cl.info()+"\n", "txt")
	case "list":
		var typ string
		var ids map[int64]bool
		for i := 2; i < len(c.Args); i++ {
			switch {
			case c.Is(i, "type") && i+1 < len(c.Args):
				typ = strings.ToLower(c.Arg(i + 1))
				switch typ {
				case "normal", "master", "replica", "slave", "pubsub":
				default:
					c.Err("ERR Unknown client type '" + c.Arg(i+1) + "'")
					return
				}
				i++
			case c.Is(i, "id") && i+1 < len(c.Args):
				ids = map[int64]bool{}
				for j := i + 1; j < len(c.Args); j++ {
					n, ok := parseInt(c.Args[j])
					if !ok || n <= 0 {
						c.Err("ERR Invalid client ID")
						return
					}
					ids[n] = true
				}
				i = len(c.Args)
			default:
				c.SyntaxErr()
				return
			}
		}
		var b strings.Builder
		for _, o := range s.sortedClients() {
			if ids != nil && !ids[o.id] {
				continue
			}
			if typ != "" && clientType(o) != typ && !(typ == "slave" && clientType(o) == "replica") {
				continue
			}
			b.WriteString(o.info())
			b.WriteByte('\n')
		}
		c.W.WriteVerbatim(b.String(), "txt")
	case "kill":
		clientKill(c)
	case "pause":
		if len(c.Args) < 3 || len(c.Args) > 4 {
			c.WrongSubArgs()
			return
		}
		ms, ok := parseInt(c.Args[2])
		if !ok || ms < 0 {
			c.Err("ERR timeout is not an integer or out of range")
			return
		}
		all := true
		if len(c.Args) == 4 {
			switch {
			case c.Is(3, "write"):
				all = false
			case c.Is(3, "all"):
			default:
				c.Err("ERR CLIENT PAUSE mode must be WRITE or ALL")
				return
			}
		}
		s.pauseAll.Store(all)
		s.pauseEnd.Store(time.Now().UnixMilli() + ms)
		c.W.WriteOK()
	case "unpause":
		s.pauseEnd.Store(0)
		c.W.WriteOK()
	case "reply":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		switch {
		case c.Is(2, "on"):
			cl.replyMode = replyOn
			c.W.WriteOK()
		case c.Is(2, "off"):
			cl.replyMode = replyOff
		case c.Is(2, "skip"):
			cl.replyMode = replySkipNext
		default:
			c.SyntaxErr()
		}
	case "no-evict", "no-touch":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		on := c.Is(2, "on")
		if !on && !c.Is(2, "off") {
			c.SyntaxErr()
			return
		}
		if sub == "no-evict" {
			cl.noEvict = on
		} else {
			cl.noTouch = on
		}
		c.W.WriteOK()
	case "unblock":
		if len(c.Args) < 3 || len(c.Args) > 4 {
			c.WrongSubArgs()
			return
		}
		id, ok := parseInt(c.Args[2])
		if !ok {
			c.Err(errNotInt)
			return
		}
		how := blockUnblockTimeout
		if len(c.Args) == 4 {
			switch {
			case c.Is(3, "timeout"):
			case c.Is(3, "error"):
				how = blockUnblockErr
			default:
				c.Err("ERR CLIENT UNBLOCK reason should be TIMEOUT or ERROR")
				return
			}
		}
		s.clientsMu.RLock()
		target := s.clients[id]
		s.clientsMu.RUnlock()
		if target == nil || !target.blockedNow.Load() {
			c.W.WriteInt(0)
			return
		}
		select {
		case target.unblockCh <- how:
			c.W.WriteInt(1)
		default:
			c.W.WriteInt(0)
		}
	case "getredir":
		c.W.WriteInt(-1)
	case "tracking":
		if len(c.Args) >= 3 && c.Is(2, "off") {
			c.W.WriteOK()
			return
		}
		c.Err("ERR client-side caching (CLIENT TRACKING) is not supported by DistCache yet")
	case "trackinginfo":
		c.W.WriteMapLen(3)
		c.W.WriteBulkString("flags")
		c.W.WriteArrayLen(1)
		c.W.WriteBulkString("off")
		c.W.WriteBulkString("redirect")
		c.W.WriteInt(-1)
		c.W.WriteBulkString("prefixes")
		c.W.WriteArrayLen(0)
	case "help":
		c.writeHelp("CLIENT", []string{
			"GETNAME", "    Return the name of the current connection.",
			"ID", "    Return the ID of the current connection.",
			"INFO", "    Return information about the current client connection.",
			"KILL <ip:port>", "    Kill connection made from <ip:port>.",
			"KILL <option> <value> [<option> <value> [...]]", "    Kill connections. Options are: ID, ADDR, LADDR, TYPE, USER, SKIPME, MAXAGE",
			"LIST [options ...]", "    Return information about client connections.",
			"UNPAUSE", "    Stop the current client pause, resuming traffic.",
			"PAUSE <timeout> [WRITE|ALL]", "    Suspend all, or just write, clients for <timeout> milliseconds.",
			"REPLY (ON|OFF|SKIP)", "    Control the replies sent to the current connection.",
			"SETNAME <name>", "    Assign the name <name> to the current connection.",
			"SETINFO <option> <value>", "    Set client meta attr. Options are: LIB-NAME, LIB-VER.",
			"UNBLOCK <clientid> [TIMEOUT|ERROR]", "    Unblock the specified blocked client.",
			"NO-EVICT (ON|OFF)", "    Protect the current client connection from eviction.",
			"NO-TOUCH (ON|OFF)", "    Will not touch LRU/LFU stats when this mode is on.",
		})
	default:
		c.UnknownSub()
	}
}

func clientType(c *Client) string {
	switch {
	case c.isMaster:
		return "master"
	case c.isReplica:
		return "replica"
	case c.inPubSub():
		return "pubsub"
	}
	return "normal"
}

func (s *Server) sortedClients() []*Client {
	s.clientsMu.RLock()
	out := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		out = append(out, c)
	}
	s.clientsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func clientKill(c *Ctx) {
	s := c.s
	if len(c.Args) == 3 {
		addr := c.Arg(2)
		for _, o := range s.sortedClients() {
			if o.addr == addr {
				o.conn.Close()
				if o == c.cl {
					c.cl.closeAfterReply = true
				}
				c.W.WriteOK()
				return
			}
		}
		c.Err("ERR No such client")
		return
	}
	if len(c.Args)%2 != 0 {
		c.SyntaxErr()
		return
	}
	var id int64
	var addr, laddr, typ, user string
	var maxAge int64
	skipme := true
	for i := 2; i < len(c.Args); i += 2 {
		v := c.Arg(i + 1)
		switch strings.ToLower(c.Arg(i)) {
		case "id":
			n, ok := parseInt(c.Args[i+1])
			if !ok || n <= 0 {
				c.Err("ERR client-id should be greater than 0")
				return
			}
			id = n
		case "addr":
			addr = v
		case "laddr":
			laddr = v
		case "type":
			typ = strings.ToLower(v)
			if typ == "slave" {
				typ = "replica"
			}
		case "user":
			user = v
		case "skipme":
			switch strings.ToLower(v) {
			case "yes":
				skipme = true
			case "no":
				skipme = false
			default:
				c.SyntaxErr()
				return
			}
		case "maxage":
			n, ok := parseInt(c.Args[i+1])
			if !ok {
				c.Err(errNotInt)
				return
			}
			maxAge = n
		default:
			c.SyntaxErr()
			return
		}
	}
	var killed int64
	for _, o := range s.sortedClients() {
		if id != 0 && o.id != id || addr != "" && o.addr != addr || laddr != "" && o.laddr != laddr ||
			typ != "" && clientType(o) != typ || user != "" && o.user != user {
			continue
		}
		if maxAge > 0 && time.Since(o.created) < time.Duration(maxAge)*time.Second {
			continue
		}
		if o == c.cl && skipme {
			continue
		}
		o.conn.Close()
		if o == c.cl {
			c.cl.closeAfterReply = true
		}
		killed++
	}
	c.W.WriteInt(killed)
}

func cmdConfig(c *Ctx) {
	cfg := c.s.cfg
	switch strings.ToLower(c.Arg(1)) {
	case "get":
		if len(c.Args) < 3 {
			c.WrongSubArgs()
			return
		}
		seen := map[string]bool{}
		var pairs [][2]string
		for _, p := range c.Args[2:] {
			for _, kv := range cfg.Get(string(p)) {
				if !seen[kv[0]] {
					seen[kv[0]] = true
					pairs = append(pairs, kv)
				}
			}
		}
		c.W.WriteMapLen(len(pairs))
		for _, kv := range pairs {
			c.W.WriteBulkString(kv[0])
			c.W.WriteBulkString(kv[1])
		}
	case "set":
		if len(c.Args) < 4 || len(c.Args)%2 != 0 {
			c.WrongSubArgs()
			return
		}
		for i := 2; i < len(c.Args); i += 2 {
			name := strings.ToLower(c.Arg(i))
			if err := cfg.Set(name, c.Arg(i+1), false); err != nil {
				c.Err("ERR " + err.Error())
				return
			}
			c.s.applyConfigChange(name)
		}
		c.W.WriteOK()
	case "resetstat":
		c.s.resetStats()
		c.W.WriteOK()
	case "rewrite":
		if err := cfg.Rewrite(); err != nil {
			c.Err("ERR Rewriting config file: " + err.Error())
			return
		}
		c.W.WriteOK()
	case "help":
		c.writeHelp("CONFIG", []string{
			"GET <pattern>", "    Return parameters matching the glob-like <pattern> and their values.",
			"SET <directive> <value>", "    Set the configuration <directive> to <value>.",
			"RESETSTAT", "    Reset statistics reported by the INFO command.",
			"REWRITE", "    Rewrite the configuration file.",
		})
	default:
		c.UnknownSub()
	}
}

// applyConfigChange reacts to runtime config changes.
func (s *Server) applyConfigChange(name string) {
	switch name {
	case "appendonly":
		if s.cfg.AppendOnly.Load() {
			go func() {
				if err := s.rewriteAOF(); err != nil {
					s.log.Warn("enabling AOF failed", "err", err)
				}
			}()
		} else {
			s.prop.closeAOF()
		}
	case "maxmemory":
		go s.performEvictions()
	case "lfu-log-factor":
		s.ks.LFULogFactor.Store(s.cfg.LFULogFactor.Load())
	case "lfu-decay-time":
		s.ks.LFUDecayTime.Store(s.cfg.LFUDecayTime.Load())
	case "requirepass":
		// Existing connections stay authenticated, like Redis.
	}
}

func (s *Server) resetStats() {
	st := &s.stats
	st.commands.Store(0)
	st.connections.Store(0)
	st.rejected.Store(0)
	st.expiredKeys.Store(0)
	st.evictedKeys.Store(0)
	st.keyspaceHits.Store(0)
	st.keyspaceMisses.Store(0)
	st.netInput.Store(0)
	st.netOutput.Store(0)
	st.pubsubMessages.Store(0)
	st.errorReplies.Store(0)
	for _, cmd := range s.cmds {
		cmd.Calls.Store(0)
		cmd.Usec.Store(0)
		cmd.Rejected.Store(0)
		cmd.Failed.Store(0)
	}
}

func cmdTime(c *Ctx) {
	now := time.Now()
	c.W.WriteArrayLen(2)
	c.W.WriteBulkString(strconv.FormatInt(now.Unix(), 10))
	c.W.WriteBulkString(strconv.FormatInt(int64(now.Nanosecond()/1000), 10))
}

func cmdLastSave(c *Ctx) { c.W.WriteInt(c.s.stats.lastSaveTime.Load()) }

func cmdSave(c *Ctx) {
	if c.s.bgsave.Load() {
		c.Err("ERR Background save already in progress")
		return
	}
	if err := c.s.saveSync(); err != nil {
		c.Err("ERR " + err.Error())
		return
	}
	c.W.WriteOK()
}

func cmdBgSave(c *Ctx) {
	if len(c.Args) == 2 && !c.Is(1, "schedule") {
		c.SyntaxErr()
		return
	}
	if err := c.s.bgSave(); err != nil {
		if len(c.Args) == 2 {
			c.W.WriteSimple("Background saving scheduled")
			return
		}
		c.Err("ERR " + err.Error())
		return
	}
	c.W.WriteSimple("Background saving started")
}

func cmdBgRewriteAOF(c *Ctx) {
	if c.s.aofRewr.Load() {
		c.Err("ERR Background append only file rewriting already in progress")
		return
	}
	go func() { _ = c.s.rewriteAOF() }()
	c.W.WriteSimple("Background append only file rewriting started")
}

func cmdShutdown(c *Ctx) {
	save := len(c.s.cfg.Save) > 0 || c.s.cfg.AppendOnly.Load()
	for i := 1; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "nosave"):
			save = false
		case c.Is(i, "save"):
			save = true
		case c.Is(i, "now"), c.Is(i, "force"):
		case c.Is(i, "abort"):
			c.Err("ERR No shutdown in progress.")
			return
		default:
			c.SyntaxErr()
			return
		}
	}
	if save && !c.s.cfg.AppendOnly.Load() {
		if err := c.s.saveSync(); err != nil {
			c.Err("ERR Errors trying to SHUTDOWN. Check logs.")
			return
		}
	}
	go func() { _ = c.s.Shutdown(false) }()
	if c.cl != nil {
		c.cl.closeAfterReply = true
	}
}

func cmdMonitor(c *Ctx) {
	cl := c.cl
	if cl.isMonitor {
		c.W.WriteOK()
		return
	}
	cl.startPush()
	c.s.monitorsMu.Lock()
	c.s.monitors[cl] = struct{}{}
	c.s.monitorN.Add(1)
	c.s.monitorsMu.Unlock()
	cl.isMonitor = true
	c.W.WriteOK()
}

func cmdMemory(c *Ctx) {
	switch strings.ToLower(c.Arg(1)) {
	case "usage":
		if len(c.Args) != 3 && len(c.Args) != 5 {
			c.SyntaxErr()
			return
		}
		if len(c.Args) == 5 && !c.Is(3, "samples") {
			c.SyntaxErr()
			return
		}
		key := c.Arg(2)
		var mem int64 = -1
		c.withKeys([]string{key}, false, func() {
			db := c.s.newDB(c.cl, false)
			db.NoTouch = true
			if e := db.Lookup(key); e != nil {
				mem = storage.EntryOverhead + int64(len(key)) + storage.ValueBytes(e.Value)
			}
		})
		if mem < 0 {
			c.W.WriteNull()
			return
		}
		c.W.WriteInt(mem)
	case "stats":
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		used := c.s.ks.UsedMemory()
		var keys int64
		for d := 0; d < c.s.ks.NumDB; d++ {
			keys += c.s.ks.DBSize(d)
		}
		pairs := []struct {
			k string
			v int64
		}{
			{"peak.allocated", int64(ms.HeapSys)}, {"total.allocated", int64(ms.HeapAlloc)},
			{"startup.allocated", 0}, {"replication.backlog", int64(c.s.repl.backlogSize())},
			{"clients.slaves", 0}, {"clients.normal", int64(c.s.clientCount()) * 32768},
			{"aof.buffer", 0}, {"keys.count", keys}, {"dataset.bytes", used},
		}
		c.W.WriteMapLen(len(pairs))
		for _, p := range pairs {
			c.W.WriteBulkString(p.k)
			c.W.WriteInt(p.v)
		}
	case "doctor":
		c.W.WriteVerbatim("Hi Sam, I can't find any memory issue in your instance. I can only account for what occurs on this base.", "txt")
	case "purge":
		debug.FreeOSMemory()
		c.W.WriteOK()
	case "malloc-stats":
		c.W.WriteVerbatim("Stats not supported for the current allocator (Go runtime)", "txt")
	case "help":
		c.writeHelp("MEMORY", []string{
			"DOCTOR", "    Return memory problems reports.",
			"MALLOC-STATS", "    Return internal statistics report from the memory allocator.",
			"PURGE", "    Attempt to purge dirty pages for reclamation by the allocator.",
			"STATS", "    Return information about the memory usage of the server.",
			"USAGE <key> [SAMPLES <count>]", "    Return memory in bytes used by <key> and its value.",
		})
	default:
		c.UnknownSub()
	}
}

func cmdLatency(c *Ctx) {
	switch strings.ToLower(c.Arg(1)) {
	case "latest":
		c.W.WriteArrayLen(0)
	case "history":
		c.W.WriteArrayLen(0)
	case "reset":
		c.W.WriteInt(0)
	case "doctor":
		c.W.WriteVerbatim("Dave, no latency spike was observed during the lifetime of this instance, nice job.", "txt")
	case "graph":
		c.Err("ERR No samples available for event '" + c.ArgOr(2) + "'")
	case "histogram":
		c.W.WriteMapLen(0)
	case "help":
		c.writeHelp("LATENCY", []string{"DOCTOR", "LATEST", "HISTORY <event>", "RESET [<event> ...]", "HISTOGRAM [COMMAND ...]"})
	default:
		c.UnknownSub()
	}
}

// ArgOr returns arg i or "".
func (c *Ctx) ArgOr(i int) string {
	if i < len(c.Args) {
		return c.Arg(i)
	}
	return ""
}

func cmdLolwut(c *Ctx) {
	c.W.WriteVerbatim("DistCache ver. "+Version+" (Redis "+RedisVersion+" compatible)\n", "txt")
}

func cmdModule(c *Ctx) {
	switch strings.ToLower(c.Arg(1)) {
	case "list":
		c.W.WriteArrayLen(0)
	case "help":
		c.writeHelp("MODULE", []string{"LIST", "    Return a list of loaded modules."})
	default:
		c.Err("ERR DistCache does not support modules")
	}
}

func cmdACL(c *Ctx) {
	switch strings.ToLower(c.Arg(1)) {
	case "whoami":
		c.W.WriteBulkString(c.cl.user)
	case "users":
		c.W.WriteStrings([]string{"default"})
	case "list":
		pass := "nopass"
		if c.s.cfg.RequirePassSet.Load() {
			pass = "#<redacted>"
		}
		c.W.WriteStrings([]string{"user default on " + pass + " sanitize-payload ~* &* +@all"})
	case "cat":
		cats := []string{"keyspace", "read", "write", "set", "sortedset", "list", "hash", "string", "bitmap",
			"hyperloglog", "geo", "stream", "pubsub", "admin", "fast", "slow", "blocking", "dangerous",
			"connection", "transaction", "scripting"}
		if len(c.Args) == 3 {
			cat := strings.ToLower(c.Arg(2))
			var names []string
			for _, cmd := range c.s.cmds {
				if cmd.Group == cat || (cat == "write" && cmd.Has(FlagWrite)) || (cat == "read" && cmd.Has(FlagReadonly)) ||
					(cat == "fast" && cmd.Has(FlagFast)) || (cat == "admin" && cmd.Has(FlagAdmin)) || (cat == "blocking" && cmd.Has(FlagBlocking)) {
					names = append(names, cmd.Name)
				}
			}
			sort.Strings(names)
			c.W.WriteStrings(names)
			return
		}
		c.W.WriteStrings(cats)
	case "log":
		c.W.WriteArrayLen(0)
	case "getuser":
		if len(c.Args) != 3 || c.Arg(2) != "default" {
			c.W.WriteNull()
			return
		}
		c.W.WriteMapLen(4)
		c.W.WriteBulkString("flags")
		c.W.WriteStrings([]string{"on", "sanitize-payload"})
		c.W.WriteBulkString("passwords")
		c.W.WriteArrayLen(0)
		c.W.WriteBulkString("commands")
		c.W.WriteBulkString("+@all")
		c.W.WriteBulkString("keys")
		c.W.WriteBulkString("~*")
	case "help":
		c.writeHelp("ACL", []string{"CAT [<category>]", "GETUSER <username>", "LIST", "LOG", "USERS", "WHOAMI"})
	default:
		c.Err("ERR DistCache supports the 'default' user only (requirepass); ACL " + strings.ToUpper(c.Arg(1)) + " is not available")
	}
}

func cmdDebug(c *Ctx) {
	switch strings.ToLower(c.Arg(1)) {
	case "sleep":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		f, ok := parseFloat(c.Args[2])
		if !ok {
			c.Err(errNotFloat)
			return
		}
		time.Sleep(time.Duration(f * float64(time.Second)))
		c.W.WriteOK()
	case "set-active-expire":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		c.s.cfg.ActiveExpire.Store(c.Arg(2) != "0")
		c.W.WriteOK()
	case "object":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		var out string
		found := false
		c.withKeys([]string{c.Arg(2)}, false, func() {
			db := c.s.newDB(c.cl, false)
			db.NoTouch = true
			if e := db.Lookup(c.Arg(2)); e != nil {
				found = true
				out = fmt.Sprintf("Value at:0x0 refcount:1 encoding:%s serializedlength:%d lru:%d lru_seconds_idle:%d",
					c.s.objectEncoding(e.Value), len(storage.EncodeValue(e.Value)), e.LRU(), c.s.ks.Clock.Load()-e.LRU())
			}
		})
		if !found {
			c.Err(errNoSuchKey)
			return
		}
		c.W.WriteSimple(out)
	case "reload":
		if err := c.s.debugReload(); err != nil {
			c.Err("ERR " + err.Error())
			return
		}
		c.W.WriteOK()
	case "log":
		c.s.log.Warn("DEBUG LOG: " + strings.Join(argStrings(c.Args[2:]), " "))
		c.W.WriteOK()
	case "change-repl-id":
		c.s.repl.changeReplID()
		c.W.WriteOK()
	case "digest":
		c.W.WriteBulkString(c.s.digest())
	case "jmap", "quicklist-packed-threshold", "stringmatch-len", "listpack", "set-disable-deny-scripts",
		"replybuffer", "dict-resizing", "pause-cron":
		c.W.WriteOK()
	case "help":
		c.writeHelp("DEBUG", []string{"SLEEP <seconds>", "SET-ACTIVE-EXPIRE <0|1>", "OBJECT <key>", "RELOAD", "LOG <message>", "DIGEST", "CHANGE-REPL-ID"})
	default:
		c.UnknownSub()
	}
}

// debugReload saves and reloads the dataset (DEBUG RELOAD).
func (s *Server) debugReload() error {
	var buf bytesBuf
	if err := s.writeSnapshot(&buf, nil); err != nil {
		return err
	}
	s.ks.HoldSnapshots()
	defer s.ks.ReleaseSnapshots()
	s.ks.LockAll(true)
	defer s.ks.UnlockAll(true)
	s.ks.FlushAll()
	_, err := s.loadSnapshot(newBufioReader(buf.b))
	return err
}

// digest returns a dataset fingerprint independent of iteration order.
func (s *Server) digest() string {
	var acc [20]byte
	s.ks.LockAll(false)
	defer s.ks.UnlockAll(false)
	empty := true
	for i := 0; i < s.ks.NumShards(); i++ {
		for d := 0; d < s.ks.NumDB; d++ {
			s.ks.ForEachInShard(i, d, func(k string, e *storage.Entry) bool {
				empty = false
				h := sha1Sum([]byte(strconv.Itoa(d) + "\x00" + k + "\x00" + string(storage.EncodeValue(canonicalValue(e.Value))) + strconv.FormatInt(e.ExpireAt, 10)))
				for j := range acc {
					acc[j] ^= h[j]
				}
				return true
			})
		}
	}
	if empty {
		return strings.Repeat("0", 40)
	}
	return fmt.Sprintf("%x", acc)
}

// canonicalValue orders unordered collections for digest purposes.
func canonicalValue(v any) any {
	switch x := v.(type) {
	case *ds.Set:
		m := x.Members()
		sort.Strings(m)
		l := ds.NewList()
		for _, s := range m {
			l.PushBack([]byte(s))
		}
		return l
	case *ds.Hash:
		var fields []string
		x.ForEach(0, func(f string, _ []byte) bool { fields = append(fields, f); return true })
		sort.Strings(fields)
		l := ds.NewList()
		for _, f := range fields {
			val, _ := x.Get(f, 0)
			l.PushBack([]byte(f))
			l.PushBack(val)
		}
		return l
	}
	return v
}

func cmdRole(c *Ctx) { c.s.repl.writeRole(c) }

// ---- INFO ----

func cmdInfo(c *Ctx) {
	sections := map[string]bool{}
	all := false
	everything := false
	if len(c.Args) == 1 {
		sections["default"] = true
	}
	for _, a := range c.Args[1:] {
		s := strings.ToLower(string(a))
		switch s {
		case "all":
			all = true
		case "everything":
			everything = true
		default:
			sections[s] = true
		}
	}
	def := sections["default"]
	want := func(name string, inDefault bool) bool {
		return everything || (all && name != "commandstats" && name != "latencystats") || sections[name] || (def && inDefault)
	}
	if all || everything {
		def = true
	}
	var b strings.Builder
	add := func(title string, kv ...any) {
		if b.Len() > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("# " + title + "\r\n")
		for i := 0; i+1 < len(kv); i += 2 {
			fmt.Fprintf(&b, "%v:%v\r\n", kv[i], kv[i+1])
		}
	}
	s := c.s
	now := time.Now()
	uptime := int64(now.Sub(s.startTime).Seconds())
	if want("server", true) {
		add("Server",
			"redis_version", RedisVersion,
			"distcache_version", Version,
			"redis_git_sha1", "00000000",
			"redis_git_dirty", 0,
			"redis_build_id", "distcache",
			"redis_mode", "standalone",
			"os", runtime.GOOS+" "+runtime.GOARCH,
			"arch_bits", strconv.IntSize,
			"multiplexing_api", "goroutines",
			"atomicvar_api", "go-atomic",
			"go_version", runtime.Version(),
			"process_id", os.Getpid(),
			"process_supervised", "no",
			"run_id", s.runID,
			"tcp_port", s.cfg.Port,
			"server_time_usec", now.UnixMicro(),
			"uptime_in_seconds", uptime,
			"uptime_in_days", uptime/86400,
			"hz", s.cfg.Hz.Load(),
			"configured_hz", s.cfg.Hz.Load(),
			"lru_clock", s.ks.Clock.Load()&((1<<24)-1),
			"executable", executable(),
			"config_file", s.cfg.ConfigFile,
			"io_threads_active", 0,
			"shards", s.ks.NumShards(),
			"gomaxprocs", runtime.GOMAXPROCS(0),
		)
	}
	if want("clients", true) {
		var blocked, pubsub, watching int
		for _, o := range s.sortedClients() {
			if o.blockedNow.Load() {
				blocked++
			}
			if o.inPubSub() {
				pubsub++
			}
			if o.watchCount.Load() > 0 {
				watching++
			}
		}
		add("Clients",
			"connected_clients", s.clientCount(),
			"cluster_connections", 0,
			"maxclients", s.cfg.MaxClients.Load(),
			"client_recent_max_input_buffer", 0,
			"client_recent_max_output_buffer", 0,
			"blocked_clients", blocked,
			"tracking_clients", 0,
			"pubsub_clients", pubsub,
			"watching_clients", watching,
			"clients_in_timeout_table", blocked,
			"total_watched_keys", s.watches.count.Load(),
			"total_blocking_keys", s.blocker.count.Load(),
		)
	}
	if want("memory", true) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		used := s.ks.UsedMemory()
		maxm := s.cfg.MaxMemory.Load()
		add("Memory",
			"used_memory", used,
			"used_memory_human", humanBytes(used),
			"used_memory_rss", ms.Sys,
			"used_memory_rss_human", humanBytes(int64(ms.Sys)),
			"used_memory_peak", ms.HeapSys,
			"used_memory_peak_human", humanBytes(int64(ms.HeapSys)),
			"used_memory_dataset", used,
			"go_heap_alloc", ms.HeapAlloc,
			"go_heap_objects", ms.HeapObjects,
			"go_gc_cycles", ms.NumGC,
			"maxmemory", maxm,
			"maxmemory_human", humanBytes(maxm),
			"maxmemory_policy", config.PolicyName(s.cfg.MaxMemoryPolicy.Load()),
			"mem_fragmentation_ratio", fmt.Sprintf("%.2f", float64(ms.Sys)/float64(max64(used, 1))),
			"mem_allocator", "go-"+runtime.Version(),
			"lazyfree_pending_objects", 0,
		)
	}
	if want("persistence", true) {
		aofSize, aofOn, _ := s.prop.aofStats()
		status := "ok"
		if !s.stats.lastBgsaveOK.Load() {
			status = "err"
		}
		aofStatus := "ok"
		if !s.stats.lastAofRewriteOK.Load() {
			aofStatus = "err"
		}
		add("Persistence",
			"loading", b2i(s.loading.Load()),
			"async_loading", 0,
			"current_cow_peak", 0,
			"rdb_changes_since_last_save", s.stats.dirtySinceSave.Load(),
			"rdb_bgsave_in_progress", b2i(s.bgsave.Load()),
			"rdb_last_save_time", s.stats.lastSaveTime.Load(),
			"rdb_last_bgsave_status", status,
			"rdb_last_bgsave_time_sec", s.stats.lastBgsaveSeconds.Load(),
			"aof_enabled", b2i(aofOn),
			"aof_rewrite_in_progress", b2i(s.aofRewr.Load()),
			"aof_rewrite_scheduled", 0,
			"aof_last_bgrewrite_status", aofStatus,
			"aof_rewrites", s.stats.aofRewrites.Load(),
			"aof_current_size", aofSize,
			"snapshot_format", "dcsnap-v1",
		)
	}
	if want("stats", true) {
		add("Stats",
			"total_connections_received", s.stats.connections.Load(),
			"total_commands_processed", s.stats.commands.Load(),
			"instantaneous_ops_per_sec", s.stats.instantaneousOps.Load(),
			"total_net_input_bytes", s.stats.netInput.Load(),
			"total_net_output_bytes", s.stats.netOutput.Load(),
			"rejected_connections", s.stats.rejected.Load(),
			"sync_full", s.stats.syncFull.Load(),
			"sync_partial_ok", s.stats.syncPartialOK.Load(),
			"sync_partial_err", s.stats.syncPartialErr.Load(),
			"expired_keys", s.stats.expiredKeys.Load(),
			"evicted_keys", s.stats.evictedKeys.Load(),
			"evicted_clients", s.stats.evictedClients.Load(),
			"total_eviction_exceeded_time", 0,
			"keyspace_hits", s.stats.keyspaceHits.Load(),
			"keyspace_misses", s.stats.keyspaceMisses.Load(),
			"pubsub_channels", s.pubsubChannels(),
			"pubsub_patterns", s.pubsubPatterns(),
			"pubsubshard_channels", s.pubsubShardChannels(),
			"total_blocking_keys", s.blocker.count.Load(),
			"total_error_replies", s.stats.errorReplies.Load(),
			"total_writes_processed", s.stats.totalWritesProc.Load(),
			"total_pubsub_messages", s.stats.pubsubMessages.Load(),
		)
	}
	if want("replication", true) {
		b.WriteString("\r\n")
		b.WriteString(s.repl.infoSection())
	}
	if want("cpu", true) {
		u, sy := cpuTimes()
		add("CPU",
			"used_cpu_sys", fmt.Sprintf("%.6f", sy),
			"used_cpu_user", fmt.Sprintf("%.6f", u),
			"goroutines", runtime.NumGoroutine(),
		)
	}
	if want("modules", true) {
		add("Modules")
	}
	if want("commandstats", false) {
		var kv []any
		names := make([]string, 0, len(s.cmds))
		for n := range s.cmds {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			cmd := s.cmds[n]
			calls := cmd.Calls.Load()
			if calls == 0 && cmd.Rejected.Load() == 0 {
				continue
			}
			usec := cmd.Usec.Load()
			per := 0.0
			if calls > 0 {
				per = float64(usec) / float64(calls)
			}
			kv = append(kv, "cmdstat_"+n, fmt.Sprintf("calls=%d,usec=%d,usec_per_call=%.2f,rejected_calls=%d,failed_calls=%d",
				calls, usec, per, cmd.Rejected.Load(), cmd.Failed.Load()))
		}
		add("Commandstats", kv...)
	}
	if want("errorstats", false) {
		add("Errorstats")
	}
	if want("cluster", true) {
		add("Cluster", "cluster_enabled", 0)
	}
	if want("keyspace", true) {
		var kv []any
		for d := 0; d < s.ks.NumDB; d++ {
			n := s.ks.DBSize(d)
			if n == 0 {
				continue
			}
			kv = append(kv, "db"+strconv.Itoa(d), fmt.Sprintf("keys=%d,expires=%d,avg_ttl=0,subexpiry=0", n, s.ks.ExpiresCount(d)))
		}
		add("Keyspace", kv...)
	}
	out := b.String()
	if out != "" {
		out += "\r\n"
	}
	c.W.WriteVerbatim(out, "txt")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func humanBytes(n int64) string {
	f := float64(n)
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.2fT", f/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.2fG", f/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2fM", f/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2fK", f/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

func (s *Server) pubsubChannels() int {
	s.pubsub.mu.RLock()
	defer s.pubsub.mu.RUnlock()
	return len(s.pubsub.channels)
}

func (s *Server) pubsubPatterns() int {
	s.pubsub.mu.RLock()
	defer s.pubsub.mu.RUnlock()
	return len(s.pubsub.patterns)
}

func (s *Server) pubsubShardChannels() int {
	s.pubsub.mu.RLock()
	defer s.pubsub.mu.RUnlock()
	return len(s.pubsub.shard)
}

func cmdWait(c *Ctx) {
	n, ok := c.Int(1)
	if !ok {
		return
	}
	to, ok := c.Int(2)
	if !ok {
		return
	}
	if to < 0 {
		c.Err(errNegTimeout)
		return
	}
	c.W.WriteInt(int64(c.s.repl.waitReplicas(int(n), to)))
}

func cmdWaitAOF(c *Ctx) {
	local, ok := c.Int(1)
	if !ok {
		return
	}
	n, ok := c.Int(2)
	if !ok {
		return
	}
	to, ok := c.Int(3)
	if !ok {
		return
	}
	_, aofOn, _ := c.s.prop.aofStats()
	if local > 0 && !aofOn {
		c.Err("ERR WAITAOF cannot be used when numlocal is set but appendonly is disabled.")
		return
	}
	if aofOn {
		c.s.prop.mu.Lock()
		c.s.prop.flushAOFLocked()
		f := c.s.prop.aofFile
		c.s.prop.mu.Unlock()
		if f != nil {
			_ = f.Sync()
		}
	}
	reps := c.s.repl.waitReplicas(int(n), to)
	c.W.WriteArrayLen(2)
	c.W.WriteInt(int64(b2i(aofOn)))
	c.W.WriteInt(int64(reps))
}

func cmdCluster(c *Ctx) { c.Err("ERR This instance has cluster support disabled") }
