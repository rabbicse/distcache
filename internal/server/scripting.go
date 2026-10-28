package server

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/lua"
	"github.com/rabbicse/distcache/internal/resp"
)

// Scripting: EVAL/EVALSHA/SCRIPT and Redis 7 FUNCTIONs, running on the
// embedded Lua 5.1 interpreter. Scripts execute atomically while holding
// every shard lock (read locks for *_RO variants, so read-only scripts run
// in parallel) and their write commands are replicated as effects, exactly
// like Redis 7.

type scriptRun struct {
	killed  atomic.Bool
	wrote   atomic.Bool
	start   time.Time
	isWrite bool
}

type luaFunction struct {
	name    string
	lib     string
	flags   []string
	noWrite bool
	desc    string
}

type luaLibrary struct {
	name  string
	code  string
	funcs map[string]*luaFunction
}

type scriptEngine struct {
	s *Server

	mu      sync.Mutex
	scripts map[string]string // sha -> body
	libs    map[string]*luaLibrary
	funcs   map[string]*luaFunction
	libVer  atomic.Int64

	runMu     sync.Mutex
	running   map[*scriptRun]struct{}
	busySince atomic.Int64

	pool sync.Pool
}

// luaVM is one pooled interpreter with its compiled caches.
type luaVM struct {
	L        *lua.State
	compiled map[string]*lua.Closure
	fnVer    int64
	fns      map[string]lua.Value
	// per execution
	ctx      *Ctx
	sc       *Client
	run      *scriptRun
	readOnly bool
	resp     int
	sha      string
	loading  *luaLibrary
}

func newScriptEngine(s *Server) *scriptEngine {
	e := &scriptEngine{s: s, scripts: map[string]string{}, libs: map[string]*luaLibrary{},
		funcs: map[string]*luaFunction{}, running: map[*scriptRun]struct{}{}}
	e.pool.New = func() any { return e.newVM() }
	return e
}

func sha1hex(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func (e *scriptEngine) newVM() *luaVM {
	vm := &luaVM{L: lua.NewState(), compiled: map[string]*lua.Closure{}, resp: 2}
	L := vm.L
	r := lua.NewTable(0, 32)
	reg := func(name string, fn func(L *lua.State, a []lua.Value) []lua.Value) {
		r.RawSet(name, &lua.GoFunction{Name: name, Fn: fn})
	}
	reg("call", func(L *lua.State, a []lua.Value) []lua.Value { return one(e.redisCall(vm, a, true)) })
	reg("pcall", func(L *lua.State, a []lua.Value) []lua.Value { return one(e.redisCall(vm, a, false)) })
	reg("error_reply", func(L *lua.State, a []lua.Value) []lua.Value {
		s, ok := firstString(a)
		if !ok {
			L.RuntimeError("wrong number or type of arguments")
		}
		t := lua.NewTable(0, 1)
		if !strings.HasPrefix(s, "-") {
			s = "-" + s
		}
		t.RawSet("err", s[1:])
		return one(t)
	})
	reg("status_reply", func(L *lua.State, a []lua.Value) []lua.Value {
		s, ok := firstString(a)
		if !ok {
			L.RuntimeError("wrong number or type of arguments")
		}
		t := lua.NewTable(0, 1)
		t.RawSet("ok", s)
		return one(t)
	})
	reg("sha1hex", func(L *lua.State, a []lua.Value) []lua.Value {
		if len(a) != 1 {
			L.RuntimeError("wrong number of arguments")
		}
		s, _ := lua.ToStringCoerce(a[0])
		return one(sha1hex(s))
	})
	reg("log", func(L *lua.State, a []lua.Value) []lua.Value {
		if len(a) < 2 {
			L.RuntimeError("redis.log() requires two arguments or more.")
		}
		var parts []string
		for _, v := range a[1:] {
			s, _ := lua.ToStringCoerce(v)
			parts = append(parts, s)
		}
		e.s.log.Info("lua: " + strings.Join(parts, " "))
		return nil
	})
	reg("setresp", func(L *lua.State, a []lua.Value) []lua.Value {
		n, ok := lua.ToNumber(firstValue(a))
		if !ok || (n != 2 && n != 3) {
			L.RuntimeError("RESP version must be 2 or 3.")
		}
		vm.resp = int(n)
		return nil
	})
	reg("set_repl", func(L *lua.State, a []lua.Value) []lua.Value { return nil })
	reg("replicate_commands", func(L *lua.State, a []lua.Value) []lua.Value { return one(true) })
	reg("breakpoint", func(L *lua.State, a []lua.Value) []lua.Value { return one(false) })
	reg("debug", func(L *lua.State, a []lua.Value) []lua.Value { return nil })
	reg("acl_check_cmd", func(L *lua.State, a []lua.Value) []lua.Value {
		if len(a) == 0 {
			L.RuntimeError("Please specify at least one argument for this redis lib call")
		}
		name, _ := lua.ToStringCoerce(a[0])
		if e.s.cmds[strings.ToLower(name)] == nil {
			L.RuntimeError("Invalid command passed to redis.acl_check_cmd()")
		}
		return one(true)
	})
	reg("register_function", func(L *lua.State, a []lua.Value) []lua.Value {
		e.registerFunction(vm, a)
		return nil
	})
	reg("setresp", func(L *lua.State, a []lua.Value) []lua.Value {
		n, ok := lua.ToNumber(firstValue(a))
		if !ok || (n != 2 && n != 3) {
			L.RuntimeError("RESP version must be 2 or 3.")
		}
		vm.resp = int(n)
		return nil
	})
	for k, v := range map[string]float64{"LOG_DEBUG": 0, "LOG_VERBOSE": 1, "LOG_NOTICE": 2, "LOG_WARNING": 3,
		"REPL_NONE": 0, "REPL_SLAVE": 1, "REPL_REPLICA": 1, "REPL_AOF": 2, "REPL_ALL": 3} {
		r.RawSet(k, v)
	}
	r.RawSet("REDIS_VERSION", RedisVersion)
	r.RawSet("REDIS_VERSION_NUM", float64(0x070400))
	L.Globals.RawSet("redis", r)
	L.Globals.RawSet("server", r)
	for _, lib := range []string{"string", "table", "math", "bit", "cjson", "cmsgpack", "redis"} {
		if t, ok := L.Globals.RawGet(lib).(*lua.Table); ok {
			t.ReadOnly = true
		}
	}
	L.Globals.RawSet("KEYS", lua.NewTable(0, 0))
	L.Globals.RawSet("ARGV", lua.NewTable(0, 0))
	L.Globals.RawSet("os", osTable())
	L.ProtectGlobals = true
	L.Hook = func() error {
		if vm.run != nil && vm.run.killed.Load() {
			return errors.New("ERR Script killed by user with SCRIPT KILL...")
		}
		if vm.run != nil && vm.run.isWrite {
			e.markBusy(vm.run)
		}
		return nil
	}
	return vm
}

func osTable() *lua.Table {
	t := lua.NewTable(0, 1)
	start := time.Now()
	t.RawSet("clock", &lua.GoFunction{Name: "clock", Fn: func(L *lua.State, a []lua.Value) []lua.Value {
		return one(time.Since(start).Seconds())
	}})
	t.ReadOnly = true
	return t
}

func one(v lua.Value) []lua.Value { return []lua.Value{v} }

func firstValue(a []lua.Value) lua.Value {
	if len(a) > 0 {
		return a[0]
	}
	return nil
}

func firstString(a []lua.Value) (string, bool) {
	if len(a) != 1 {
		return "", false
	}
	s, ok := a[0].(string)
	return s, ok
}

func (e *scriptEngine) markBusy(run *scriptRun) {
	thr := time.Duration(e.s.cfg.BusyReplyThreshold.Load()) * time.Millisecond
	if thr > 0 && time.Since(run.start) > thr {
		e.busySince.CompareAndSwap(0, run.start.UnixMilli())
	}
}

// busy reports whether a write script exceeded busy-reply-threshold.
func (e *scriptEngine) busy() bool { return e.busySince.Load() != 0 }

// ---- value conversion ----

func luaArgToBytes(v lua.Value) ([]byte, bool) {
	switch x := v.(type) {
	case string:
		return []byte(x), true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e17 {
			return []byte(strconv.FormatInt(int64(x), 10)), true
		}
		return []byte(strconv.FormatFloat(x, 'g', 17, 64)), true
	}
	return nil, false
}

func errTable(msg string) *lua.Table {
	t := lua.NewTable(0, 1)
	t.RawSet("err", msg)
	return t
}

// respToLua converts a reply into a Lua value (RESP2 or RESP3 rules).
func respToLua(v resp.Value, proto int) lua.Value {
	switch v.Type {
	case ':':
		return float64(v.Int)
	case '$', '=':
		if v.Null {
			if proto >= 3 {
				return nil
			}
			return false
		}
		if v.Type == '=' && proto >= 3 {
			t := lua.NewTable(0, 1)
			vs := lua.NewTable(0, 2)
			s := string(v.Str)
			if len(s) >= 4 {
				vs.RawSet("format", s[:3])
				vs.RawSet("string", s[4:])
			} else {
				vs.RawSet("format", "txt")
				vs.RawSet("string", s)
			}
			t.RawSet("verbatim_string", vs)
			return t
		}
		if v.Type == '=' && len(v.Str) >= 4 {
			return string(v.Str[4:])
		}
		return string(v.Str)
	case '+':
		t := lua.NewTable(0, 1)
		t.RawSet("ok", string(v.Str))
		return t
	case '-':
		return errTable(string(v.Str))
	case '_':
		if proto >= 3 {
			return nil
		}
		return false
	case ',':
		if proto >= 3 {
			t := lua.NewTable(0, 1)
			t.RawSet("double", v.Float)
			return t
		}
		return string(v.Str)
	case '#':
		if proto >= 3 {
			return v.Int == 1
		}
		return float64(v.Int)
	case '(':
		t := lua.NewTable(0, 1)
		t.RawSet("big_number", string(v.Str))
		return t
	case '*', '>':
		if v.Null {
			if proto >= 3 {
				return nil
			}
			return false
		}
		t := lua.NewTable(len(v.Elems), 0)
		for i, el := range v.Elems {
			t.RawSet(float64(i+1), respToLua(el, proto))
		}
		return t
	case '~':
		inner := lua.NewTable(0, len(v.Elems))
		for _, el := range v.Elems {
			k := respToLua(el, proto)
			if k != nil {
				inner.RawSet(k, true)
			}
		}
		if proto < 3 {
			t := lua.NewTable(len(v.Elems), 0)
			for i, el := range v.Elems {
				t.RawSet(float64(i+1), respToLua(el, proto))
			}
			return t
		}
		t := lua.NewTable(0, 1)
		t.RawSet("set", inner)
		return t
	case '%':
		if proto < 3 {
			t := lua.NewTable(len(v.Elems), 0)
			for i, el := range v.Elems {
				t.RawSet(float64(i+1), respToLua(el, proto))
			}
			return t
		}
		inner := lua.NewTable(0, len(v.Elems)/2)
		for i := 0; i+1 < len(v.Elems); i += 2 {
			k := respToLua(v.Elems[i], proto)
			if k != nil {
				inner.RawSet(k, respToLua(v.Elems[i+1], proto))
			}
		}
		t := lua.NewTable(0, 1)
		t.RawSet("map", inner)
		return t
	}
	return nil
}

// luaToResp writes a Lua value as a reply following Redis conversion rules.
func luaToResp(w *resp.Writer, v lua.Value, depth int) {
	if depth > 128 {
		w.WriteError("ERR reached lua stack limit")
		return
	}
	switch x := v.(type) {
	case nil:
		w.WriteNull()
	case bool:
		if w.Proto >= 3 {
			w.WriteBool(x)
		} else if x {
			w.WriteInt(1)
		} else {
			w.WriteNull()
		}
	case float64:
		w.WriteInt(int64(x))
	case string:
		w.WriteBulkString(x)
	case *lua.Table:
		if errV := x.RawGet("err"); errV != nil {
			if s, ok := errV.(string); ok {
				w.WriteError(normaliseErr(s))
				return
			}
		}
		if okV := x.RawGet("ok"); okV != nil {
			if s, ok := okV.(string); ok {
				w.WriteSimple(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
				return
			}
		}
		if d, ok := x.RawGet("double").(float64); ok {
			w.WriteDouble(d)
			return
		}
		if bn, ok := x.RawGet("big_number").(string); ok {
			if w.Proto >= 3 {
				w.WriteRaw([]byte("(" + bn + "\r\n"))
			} else {
				w.WriteBulkString(bn)
			}
			return
		}
		if m, ok := x.RawGet("map").(*lua.Table); ok {
			var keys, vals []lua.Value
			var k lua.Value
			for {
				nk, nv, _ := m.Next(k)
				if nk == nil {
					break
				}
				keys = append(keys, nk)
				vals = append(vals, nv)
				k = nk
			}
			w.WriteMapLen(len(keys))
			for i := range keys {
				luaToResp(w, keys[i], depth+1)
				luaToResp(w, vals[i], depth+1)
			}
			return
		}
		if st, ok := x.RawGet("set").(*lua.Table); ok {
			var keys []lua.Value
			var k lua.Value
			for {
				nk, _, _ := st.Next(k)
				if nk == nil {
					break
				}
				keys = append(keys, nk)
				k = nk
			}
			w.WriteSetLen(len(keys))
			for _, kk := range keys {
				luaToResp(w, kk, depth+1)
			}
			return
		}
		if vs, ok := x.RawGet("verbatim_string").(*lua.Table); ok {
			s, _ := vs.RawGet("string").(string)
			f, _ := vs.RawGet("format").(string)
			if f == "" {
				f = "txt"
			}
			w.WriteVerbatim(s, f)
			return
		}
		n := 0
		for x.RawGet(float64(n+1)) != nil {
			n++
		}
		w.WriteArrayLen(n)
		for i := 1; i <= n; i++ {
			luaToResp(w, x.RawGet(float64(i)), depth+1)
		}
	default:
		w.WriteNull()
	}
}

// normaliseErr makes sure an error message carries an error code.
func normaliseErr(s string) string {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return "ERR"
	}
	return s
}

// ---- redis.call ----

func (e *scriptEngine) redisCall(vm *luaVM, a []lua.Value, raise bool) lua.Value {
	L := vm.L
	fail := func(msg string) lua.Value {
		if raise {
			L.Error(errTable(msg))
		}
		return errTable(msg)
	}
	if len(a) == 0 {
		return fail("ERR Please specify at least one argument for this redis lib call")
	}
	args := make([][]byte, len(a))
	for i, v := range a {
		b, ok := luaArgToBytes(v)
		if !ok {
			return fail("ERR Lua redis lib command arguments must be strings or integers")
		}
		args[i] = b
	}
	if vm.ctx == nil {
		return fail("ERR redis.call is not available while loading a library")
	}
	s := e.s
	cmd := s.lookupCommand(args[0])
	if cmd == nil {
		return fail("ERR Unknown Redis command called from script")
	}
	if (cmd.Arity > 0 && len(args) != cmd.Arity) || (cmd.Arity < 0 && len(args) < -cmd.Arity) {
		return fail("ERR Wrong number of args calling Redis command from script")
	}
	if cmd.Has(FlagNoScript) {
		return fail("ERR This Redis command is not allowed from script")
	}
	if cmd.Has(FlagWrite) {
		if vm.readOnly {
			return fail("ERR Write commands are not allowed from read-only scripts.")
		}
		if s.repl.isReplica() && s.cfg.ReplicaReadOnly.Load() && !vm.sc.isMaster {
			return fail(errReadonly)
		}
		if cmd.Has(FlagDenyOOM) && s.cfg.MaxMemory.Load() > 0 && s.ks.UsedMemory() > s.cfg.MaxMemory.Load() &&
			!vm.run.wrote.Load() && s.cfg.MaxMemoryPolicy.Load() == 0 {
			return fail(errOOM)
		}
	}
	w := resp.NewWriter(nil, vm.resp)
	s.feedMonitorsRaw(vm.sc.db, "", args, true)
	ctx := s.call(vm.sc, cmd, args, w, vm.ctx.unit, true, true)
	if ctx.dirty > 0 && cmd.Has(FlagWrite) {
		vm.run.wrote.Store(true)
	}
	r := resp.NewReader(bytes.NewReader(w.Bytes()), len(w.Bytes())+16)
	r.MaxBulkLen = 1 << 62
	val, err := r.ReadValue()
	if err != nil {
		return fail("ERR internal reply conversion error")
	}
	if val.Type == '-' {
		if raise {
			L.Error(errTable(string(val.Str)))
		}
		return errTable(string(val.Str))
	}
	return respToLua(val, vm.resp)
}

// ---- execution ----

func (e *scriptEngine) getVM() *luaVM { return e.pool.Get().(*luaVM) }

func (e *scriptEngine) putVM(vm *luaVM) {
	vm.ctx, vm.sc, vm.run, vm.loading = nil, nil, nil, nil
	vm.resp = 2
	e.pool.Put(vm)
}

func (e *scriptEngine) startRun(isWrite bool) *scriptRun {
	run := &scriptRun{start: time.Now(), isWrite: isWrite}
	e.runMu.Lock()
	e.running[run] = struct{}{}
	e.runMu.Unlock()
	return run
}

func (e *scriptEngine) endRun(run *scriptRun) {
	e.runMu.Lock()
	delete(e.running, run)
	e.runMu.Unlock()
	if run.isWrite {
		e.busySince.Store(0)
	}
}

func (e *scriptEngine) scriptClient(c *Ctx) *Client {
	sc := &Client{s: e.s, proto: 2, authed: true, user: "default", unblockCh: make(chan int, 1)}
	if c.cl != nil {
		sc.db = c.cl.db
		sc.id = c.cl.id
		sc.isMaster = c.cl.isMaster
		sc.addr = c.cl.addr
		sc.noTouch = c.cl.noTouch
	}
	sc.name.Store("")
	sc.libName.Store("")
	sc.libVer.Store("")
	sc.lastCmd.Store("")
	return sc
}

// parseNumKeys validates the numkeys argument at position 2.
func parseNumKeys(c *Ctx) (int, bool) {
	n, ok := parseInt(c.Args[2])
	if !ok {
		c.Err(errNotInt)
		return 0, false
	}
	if n < 0 {
		c.Err("ERR Number of keys can't be negative")
		return 0, false
	}
	if n > int64(len(c.Args)-3) {
		c.Err("ERR Number of keys can't be greater than number of args")
		return 0, false
	}
	return int(n), true
}

func argTable(args [][]byte) *lua.Table {
	t := lua.NewTable(len(args), 0)
	for i, a := range args {
		t.RawSet(float64(i+1), string(a))
	}
	return t
}

func (c *Ctx) scriptSelect(n int) {
	if c.cl != nil {
		c.cl.db = n
	}
}

// runScript executes fn (already compiled for vm) and writes its reply.
func (e *scriptEngine) runScript(c *Ctx, vm *luaVM, fn lua.Value, args []lua.Value, sha string, readOnly bool) {
	run := e.startRun(!readOnly)
	defer e.endRun(run)
	vm.ctx = c
	vm.sc = e.scriptClient(c)
	vm.run = run
	vm.readOnly = readOnly
	vm.resp = 2
	vm.sha = sha
	rets, err := vm.L.PCall(fn, args...)
	w := c.W
	if err != nil {
		c.errored = true
		var msg string
		var le *lua.LuaError
		var fe *lua.FatalError
		switch {
		case errors.As(err, &fe):
			msg = fe.Msg
		case errors.As(err, &le):
			if t, ok := le.Value.(*lua.Table); ok {
				if s, ok := t.RawGet("err").(string); ok {
					msg = normaliseErr(s)
				}
			}
			if msg == "" {
				msg = "ERR " + le.Error()
			}
		default:
			msg = "ERR " + err.Error()
		}
		w.WriteError(fmt.Sprintf("%s script: %s, on @user_script:%d.", msg, sha, vm.L.Line()))
		return
	}
	var v lua.Value
	if len(rets) > 0 {
		v = rets[0]
	}
	luaToResp(w, v, 0)
}

func evalGeneric(c *Ctx, bySha, readOnly bool) {
	e := c.s.scripts
	numkeys, ok := parseNumKeys(c)
	if !ok {
		return
	}
	var body, sha string
	if bySha {
		sha = strings.ToLower(c.Arg(1))
		e.mu.Lock()
		b, found := e.scripts[sha]
		e.mu.Unlock()
		if !found {
			c.Err("NOSCRIPT No matching script. Please use EVAL.")
			return
		}
		body = b
	} else {
		body = c.Arg(1)
		sha = sha1hex(body)
	}
	vm := e.getVM()
	defer e.putVM(vm)
	fn := vm.compiled[sha]
	if fn == nil {
		cl, err := vm.L.Compile(body, "user_script")
		if err != nil {
			c.Err("ERR Error compiling script (new function): " + err.Error())
			return
		}
		fn = cl
		vm.compiled[sha] = cl
	}
	e.mu.Lock()
	e.scripts[sha] = body
	e.mu.Unlock()
	if !readOnly && hasShebangNoWrites(body) {
		readOnly = true
	}
	vm.L.Globals.RawSet("KEYS", argTable(c.Args[3:3+numkeys]))
	vm.L.Globals.RawSet("ARGV", argTable(c.Args[3+numkeys:]))
	e.runScript(c, vm, fn, nil, sha, readOnly)
}

var shebangRe = regexp.MustCompile(`^#!lua(.*)`)

func hasShebangNoWrites(body string) bool {
	if !strings.HasPrefix(body, "#!") {
		return false
	}
	line, _, _ := strings.Cut(body, "\n")
	m := shebangRe.FindStringSubmatch(line)
	return m != nil && strings.Contains(m[1], "no-writes")
}

func cmdEval(c *Ctx)      { evalGeneric(c, false, false) }
func cmdEvalSha(c *Ctx)   { evalGeneric(c, true, false) }
func cmdEvalRO(c *Ctx)    { evalGeneric(c, false, true) }
func cmdEvalShaRO(c *Ctx) { evalGeneric(c, true, true) }

func (e *scriptEngine) kill(isFunction bool) string {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if len(e.running) == 0 {
		return "NOTBUSY No scripts in execution right now."
	}
	for run := range e.running {
		if run.wrote.Load() {
			return "UNKILLABLE Sorry the script already executed write commands against the dataset. You can either wait the script termination or kill the server in a hard way using the SHUTDOWN NOSAVE command."
		}
	}
	for run := range e.running {
		run.killed.Store(true)
	}
	_ = isFunction
	return ""
}

func cmdScript(c *Ctx) {
	e := c.s.scripts
	switch strings.ToLower(c.Arg(1)) {
	case "load":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		body := c.Arg(2)
		vm := e.getVM()
		_, err := vm.L.Compile(body, "user_script")
		e.putVM(vm)
		if err != nil {
			c.Err("ERR Error compiling script (new function): " + err.Error())
			return
		}
		sha := sha1hex(body)
		e.mu.Lock()
		e.scripts[sha] = body
		e.mu.Unlock()
		c.W.WriteBulkString(sha)
	case "exists":
		if len(c.Args) < 3 {
			c.WrongSubArgs()
			return
		}
		e.mu.Lock()
		c.W.WriteArrayLen(len(c.Args) - 2)
		for _, a := range c.Args[2:] {
			if _, ok := e.scripts[strings.ToLower(string(a))]; ok {
				c.W.WriteInt(1)
			} else {
				c.W.WriteInt(0)
			}
		}
		e.mu.Unlock()
	case "flush":
		if len(c.Args) > 3 || (len(c.Args) == 3 && !c.Is(2, "async") && !c.Is(2, "sync")) {
			c.SyntaxErr()
			return
		}
		e.mu.Lock()
		e.scripts = map[string]string{}
		e.mu.Unlock()
		// Drop compiled caches by replacing the pool.
		e.pool = sync.Pool{New: func() any { return e.newVM() }}
		c.W.WriteOK()
	case "kill":
		if msg := e.kill(false); msg != "" {
			c.Err(msg)
			return
		}
		c.W.WriteOK()
	case "help":
		c.writeHelp("SCRIPT", []string{
			"DEBUG (YES|SYNC|NO)", "    Set the debug mode for subsequent scripts executed.",
			"EXISTS <sha1> [<sha1> ...]", "    Return information about the existence of the scripts in the script cache.",
			"FLUSH [ASYNC|SYNC]", "    Flush the Lua scripts cache. Very dangerous on replicas.",
			"KILL", "    Kill the currently executing Lua script.",
			"LOAD <script>", "    Load a script into the scripts cache without executing it.",
		})
	case "debug":
		c.Err("ERR SCRIPT DEBUG is not supported by DistCache")
	default:
		c.UnknownSub()
	}
}

// ---- FUNCTION ----

var fnNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func (e *scriptEngine) registerFunction(vm *luaVM, a []lua.Value) {
	L := vm.L
	if vm.loading == nil {
		L.RuntimeError("redis.register_function can only be called on FUNCTION LOAD command")
	}
	var name string
	var cb lua.Value
	var flags []string
	if len(a) == 1 {
		t, ok := a[0].(*lua.Table)
		if !ok {
			L.RuntimeError("calling redis.register_function with a single argument is only applicable to Lua table (representing named arguments).")
		}
		var k lua.Value
		for {
			nk, nv, _ := t.Next(k)
			if nk == nil {
				break
			}
			ks, _ := nk.(string)
			switch ks {
			case "function_name":
				name, _ = nv.(string)
			case "callback":
				cb = nv
			case "flags":
				ft, ok := nv.(*lua.Table)
				if !ok {
					L.RuntimeError("flags argument to redis.register_function must be a table representing function flags")
				}
				for i := 1; ft.RawGet(float64(i)) != nil; i++ {
					f, _ := ft.RawGet(float64(i)).(string)
					switch f {
					case "no-writes", "allow-oom", "allow-stale", "no-cluster", "allow-cross-slot-keys":
					default:
						L.RuntimeError("unknown flag given")
					}
					flags = append(flags, f)
				}
			case "description":
			default:
				L.RuntimeError("unknown argument given to redis.register_function")
			}
			k = nk
		}
	} else if len(a) == 2 {
		name, _ = a[0].(string)
		cb = a[1]
	} else {
		L.RuntimeError("wrong number of arguments to redis.register_function")
	}
	if name == "" || !fnNameRe.MatchString(name) {
		L.RuntimeError("Library names can only contain letters, numbers, or underscores(_) and must be at least one character long")
	}
	switch cb.(type) {
	case *lua.Closure, *lua.GoFunction:
	default:
		L.RuntimeError("callback argument given to redis.register_function must be a function")
	}
	if _, dup := vm.loading.funcs[name]; dup {
		L.RuntimeError("Function already exists in the library")
	}
	f := &luaFunction{name: name, lib: vm.loading.name, flags: flags}
	for _, fl := range flags {
		if fl == "no-writes" {
			f.noWrite = true
		}
	}
	vm.loading.funcs[name] = f
	if vm.fns == nil {
		vm.fns = map[string]lua.Value{}
	}
	vm.fns[vm.loading.name+"\x00"+name] = cb
}

var libMetaRe = regexp.MustCompile(`^#!(\w+)(.*)$`)

func parseLibMeta(code string) (engine, name string, err error) {
	line, _, _ := strings.Cut(code, "\n")
	if !strings.HasPrefix(line, "#!") {
		return "", "", errors.New("ERR Missing library metadata")
	}
	m := libMetaRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", errors.New("ERR Invalid metadata value given: " + line)
	}
	engine = m[1]
	for _, f := range strings.Fields(m[2]) {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k != "name" {
			return "", "", errors.New("ERR Invalid metadata value given: " + f)
		}
		name = v
	}
	if !strings.EqualFold(engine, "lua") {
		return "", "", fmt.Errorf("ERR Engine '%s' not found", engine)
	}
	if name == "" {
		return "", "", errors.New("ERR Library name was not given")
	}
	if !fnNameRe.MatchString(name) {
		return "", "", errors.New("ERR Library names can only contain letters, numbers, or underscores(_) and must be at least one character long")
	}
	return engine, name, nil
}

// loadLibraryInto compiles and runs library code inside vm, registering
// its functions. It returns the library definition.
func (e *scriptEngine) loadLibraryInto(vm *luaVM, name, code string) (*luaLibrary, error) {
	lib := &luaLibrary{name: name, code: code, funcs: map[string]*luaFunction{}}
	body := code
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = "\n" + body[i+1:]
	} else {
		body = ""
	}
	cl, err := vm.L.Compile(body, "user_function")
	if err != nil {
		return nil, errors.New("ERR Error compiling function: " + err.Error())
	}
	vm.loading = lib
	saved := vm.ctx
	vm.ctx = nil
	run := &scriptRun{start: time.Now()}
	vm.run = run
	_, err = vm.L.PCall(cl)
	vm.loading = nil
	vm.ctx = saved
	if err != nil {
		return nil, errors.New("ERR Error registering functions: " + err.Error())
	}
	return lib, nil
}

func (e *scriptEngine) functionLoad(code string, replace bool) (string, error) {
	_, name, err := parseLibMeta(code)
	if err != nil {
		return "", err
	}
	vm := e.newVM()
	lib, err := e.loadLibraryInto(vm, name, code)
	if err != nil {
		return "", err
	}
	if len(lib.funcs) == 0 {
		return "", errors.New("ERR No functions registered")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old := e.libs[name]
	if old != nil && !replace {
		return "", fmt.Errorf("ERR Library '%s' already exists", name)
	}
	for fn := range lib.funcs {
		if f, ok := e.funcs[fn]; ok && f.lib != name {
			return "", fmt.Errorf("ERR Function %s already exists", fn)
		}
	}
	if old != nil {
		for fn := range old.funcs {
			delete(e.funcs, fn)
		}
	}
	e.libs[name] = lib
	for fn, f := range lib.funcs {
		e.funcs[fn] = f
	}
	e.libVer.Add(1)
	return name, nil
}

// vmFunction returns the callable for a function, (re)loading libraries
// into the VM when the library set changed.
func (e *scriptEngine) vmFunction(vm *luaVM, f *luaFunction) (lua.Value, error) {
	ver := e.libVer.Load()
	if vm.fnVer != ver || vm.fns == nil {
		vm.fns = map[string]lua.Value{}
		e.mu.Lock()
		libs := make([]*luaLibrary, 0, len(e.libs))
		for _, l := range e.libs {
			libs = append(libs, l)
		}
		e.mu.Unlock()
		for _, l := range libs {
			if _, err := e.loadLibraryInto(vm, l.name, l.code); err != nil {
				return nil, err
			}
		}
		vm.fnVer = ver
	}
	fn := vm.fns[f.lib+"\x00"+f.name]
	if fn == nil {
		return nil, errors.New("ERR Function not found")
	}
	return fn, nil
}

func fcallGeneric(c *Ctx, readOnly bool) {
	e := c.s.scripts
	numkeys, ok := parseNumKeys(c)
	if !ok {
		return
	}
	e.mu.Lock()
	f := e.funcs[c.Arg(1)]
	e.mu.Unlock()
	if f == nil {
		c.Err("ERR Function not found")
		return
	}
	if readOnly && !f.noWrite {
		c.Err("ERR Can not execute a script with write flag using *_ro command.")
		return
	}
	vm := e.getVM()
	defer e.putVM(vm)
	fn, err := e.vmFunction(vm, f)
	if err != nil {
		c.Err(err.Error())
		return
	}
	keys := argTable(c.Args[3 : 3+numkeys])
	argv := argTable(c.Args[3+numkeys:])
	e.runScript(c, vm, fn, []lua.Value{keys, argv}, "fcall:"+f.name, readOnly || f.noWrite)
}

func cmdFCall(c *Ctx)   { fcallGeneric(c, false) }
func cmdFCallRO(c *Ctx) { fcallGeneric(c, true) }

func cmdFunction(c *Ctx) {
	e := c.s.scripts
	switch strings.ToLower(c.Arg(1)) {
	case "load":
		replace := false
		i := 2
		if len(c.Args) == 4 && c.Is(2, "replace") {
			replace = true
			i = 3
		} else if len(c.Args) != 3 {
			c.SyntaxErr()
			return
		}
		name, err := e.functionLoad(c.Arg(i), replace)
		if err != nil {
			c.Err(err.Error())
			return
		}
		c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
		c.s.stats.dirtySinceSave.Add(1)
		c.W.WriteBulkString(name)
	case "delete":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		e.mu.Lock()
		lib := e.libs[c.Arg(2)]
		if lib == nil {
			e.mu.Unlock()
			c.Err("ERR Library not found")
			return
		}
		for fn := range lib.funcs {
			delete(e.funcs, fn)
		}
		delete(e.libs, lib.name)
		e.libVer.Add(1)
		e.mu.Unlock()
		c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
		c.W.WriteOK()
	case "flush":
		e.mu.Lock()
		e.libs = map[string]*luaLibrary{}
		e.funcs = map[string]*luaFunction{}
		e.libVer.Add(1)
		e.mu.Unlock()
		c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
		c.W.WriteOK()
	case "list":
		pattern := ""
		withCode := false
		for i := 2; i < len(c.Args); i++ {
			switch {
			case c.Is(i, "withcode"):
				withCode = true
			case c.Is(i, "libraryname") && i+1 < len(c.Args):
				pattern = c.Arg(i + 1)
				i++
			default:
				c.SyntaxErr()
				return
			}
		}
		e.mu.Lock()
		var libs []*luaLibrary
		for _, l := range e.libs {
			if pattern == "" || globMatchLower(pattern, l.name) {
				libs = append(libs, l)
			}
		}
		e.mu.Unlock()
		sort.Slice(libs, func(i, j int) bool { return libs[i].name < libs[j].name })
		c.W.WriteArrayLen(len(libs))
		for _, l := range libs {
			n := 3
			if withCode {
				n = 4
			}
			c.W.WriteMapLen(n)
			c.W.WriteBulkString("library_name")
			c.W.WriteBulkString(l.name)
			c.W.WriteBulkString("engine")
			c.W.WriteBulkString("LUA")
			c.W.WriteBulkString("functions")
			names := make([]string, 0, len(l.funcs))
			for fn := range l.funcs {
				names = append(names, fn)
			}
			sort.Strings(names)
			c.W.WriteArrayLen(len(names))
			for _, fn := range names {
				f := l.funcs[fn]
				c.W.WriteMapLen(3)
				c.W.WriteBulkString("name")
				c.W.WriteBulkString(fn)
				c.W.WriteBulkString("description")
				c.W.WriteNull()
				c.W.WriteBulkString("flags")
				c.W.WriteSetLen(len(f.flags))
				for _, fl := range f.flags {
					c.W.WriteBulkString(fl)
				}
			}
			if withCode {
				c.W.WriteBulkString("library_code")
				c.W.WriteBulkString(l.code)
			}
		}
	case "stats":
		e.mu.Lock()
		nl, nf := len(e.libs), len(e.funcs)
		e.mu.Unlock()
		c.W.WriteMapLen(2)
		c.W.WriteBulkString("running_script")
		c.W.WriteNull()
		c.W.WriteBulkString("engines")
		c.W.WriteMapLen(1)
		c.W.WriteBulkString("LUA")
		c.W.WriteMapLen(2)
		c.W.WriteBulkString("libraries_count")
		c.W.WriteInt(int64(nl))
		c.W.WriteBulkString("functions_count")
		c.W.WriteInt(int64(nf))
	case "dump":
		c.W.WriteBulk(e.dumpLibraries())
	case "restore":
		if len(c.Args) < 3 || len(c.Args) > 4 {
			c.WrongSubArgs()
			return
		}
		policy := "append"
		if len(c.Args) == 4 {
			policy = strings.ToLower(c.Arg(3))
			if policy != "append" && policy != "replace" && policy != "flush" {
				c.Err("ERR Wrong restore policy given, value should be either FLUSH, APPEND or REPLACE.")
				return
			}
		}
		if err := e.restoreLibraries(c.Args[2], policy); err != nil {
			c.Err(err.Error())
			return
		}
		c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
		c.W.WriteOK()
	case "kill":
		if msg := e.kill(true); msg != "" {
			c.Err(msg)
			return
		}
		c.W.WriteOK()
	case "help":
		c.writeHelp("FUNCTION", []string{
			"LOAD [REPLACE] <FUNCTION CODE>", "    Create a new library with the given library name and code.",
			"DELETE <LIBRARY NAME>", "    Delete the given library.",
			"LIST [LIBRARYNAME PATTERN] [WITHCODE]", "    Return general information on all the libraries.",
			"FLUSH [ASYNC|SYNC]", "    Delete all the libraries.",
			"DUMP", "    Return a serialized payload representing the current libraries.",
			"RESTORE <PAYLOAD> [FLUSH|APPEND|REPLACE]", "    Restore the libraries represented by the given payload.",
			"STATS", "    Return information about the current function running.",
			"KILL", "    Kill the current running function.",
		})
	default:
		c.UnknownSub()
	}
}

func globMatchLower(pattern, s string) bool {
	return matchGlob(pattern, s)
}

// dumpLibraries serialises all libraries (DCFN payload).
func (e *scriptEngine) dumpLibraries() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b bytes.Buffer
	b.WriteString("DCFN1")
	names := make([]string, 0, len(e.libs))
	for n := range e.libs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		code := e.libs[n].code
		b.WriteString(strconv.Itoa(len(code)))
		b.WriteByte(':')
		b.WriteString(code)
	}
	return b.Bytes()
}

func (e *scriptEngine) restoreLibraries(payload []byte, policy string) error {
	if !bytes.HasPrefix(payload, []byte("DCFN1")) {
		return errors.New("ERR payload version or checksum are wrong")
	}
	br := bufio.NewReader(bytes.NewReader(payload[5:]))
	var codes []string
	for {
		lenStr, err := br.ReadString(':')
		if err != nil {
			break
		}
		n, err := strconv.Atoi(strings.TrimSuffix(lenStr, ":"))
		if err != nil || n < 0 {
			return errors.New("ERR payload version or checksum are wrong")
		}
		buf := make([]byte, n)
		if _, err := ioReadFull(br, buf); err != nil {
			return errors.New("ERR payload version or checksum are wrong")
		}
		codes = append(codes, string(buf))
	}
	if policy == "flush" {
		e.mu.Lock()
		e.libs = map[string]*luaLibrary{}
		e.funcs = map[string]*luaFunction{}
		e.mu.Unlock()
	}
	for _, code := range codes {
		if _, err := e.functionLoad(code, policy == "replace"); err != nil {
			return err
		}
	}
	e.libVer.Add(1)
	return nil
}

// libraryCodes returns all library sources (for snapshots).
func (e *scriptEngine) libraryCodes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.libs))
	for n := range e.libs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, e.libs[n].code)
	}
	return out
}
