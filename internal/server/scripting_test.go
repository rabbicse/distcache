// Integration and differential tests for EVAL/EVALSHA/SCRIPT and the Redis 7
// FUNCTION family, including Lua-to-RESP type mapping and script killing.
package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

const (
	noScript = "-NOSCRIPT No matching script. Please use EVAL."
	// sha1 of "return 1", loaded by the SCRIPT LOAD step below.
	sha1Return1 = "e0e1f9fabfc9d4800c877a703b823ac0578ff8db"
	// sha1 of "return 2", never loaded.
	sha1Unknown = "7f923f79fe76194c868d7e1d0820de36700eb649"
)

// evalTypesScript covers the Lua to RESP2 conversion table, KEYS/ARGV and the
// script cache commands.
var evalTypesScript = []testutil.Step{
	testutil.S(":1", "EVAL", "return 1", "0"),
	testutil.S(":3", "EVAL", "return 3.9", "0"),
	testutil.S(":-3", "EVAL", "return -3.9", "0"),
	testutil.S(`"str"`, "EVAL", "return 'str'", "0"),
	testutil.S(":1", "EVAL", "return true", "0"),
	testutil.S("(nil)", "EVAL", "return false", "0"),
	testutil.S("(nil)", "EVAL", "return nil", "0"),
	testutil.S("(nil)", "EVAL", "return", "0"),
	testutil.S(`*[:1, :2, "a"]`, "EVAL", "return {1, 2, 'a'}", "0"),
	testutil.S(`*[:1, :2]`, "EVAL", "return {1, 2, nil, 4}", "0"),
	testutil.S(`*[]`, "EVAL", "return {}", "0"),
	testutil.S(`*[:1, *[:2, *[:3]]]`, "EVAL", "return {1, {2, {3}}}", "0"),
	testutil.S("+fine", "EVAL", "return {ok='fine'}", "0"),
	testutil.S("-My Error", "EVAL", "return {err='My Error'}", "0"),
	testutil.S("+OK", "EVAL", "return redis.status_reply('OK')", "0"),
	testutil.S("-ERR custom", "EVAL", "return redis.error_reply('ERR custom')", "0"),
	testutil.S(`*["k1", "k2", "a1", "a2"]`, "EVAL", "return {KEYS[1], KEYS[2], ARGV[1], ARGV[2]}", "2", "k1", "k2", "a1", "a2"),
	testutil.S(":2", "EVAL", "return #KEYS", "2", "k1", "k2"),
	testutil.S(":0", "EVAL", "return #ARGV", "0"),
	testutil.S(`"string"`, "EVAL", "return type(KEYS[1])", "1", "k"),
	testutil.S(`"n"`, "EVAL", "return string.rep('n', 1)", "0"),
	testutil.S(`"3"`, "EVAL", "return tostring(1 + 2)", "0"),
	testutil.S(`"da39a3ee5e6b4b0d3255bfef95601890afd80709"`, "EVAL", "return redis.sha1hex('')", "0"),
	testutil.S(`"86f7e437faa5a7fce15d1ddcb9eaeaea377667b8"`, "EVAL", "return redis.sha1hex('a')", "0"),
	testutil.S(`"[1,2,3]"`, "EVAL", "return cjson.encode({1, 2, 3})", "0"),
	testutil.S(`"{\"a\":1}"`, "EVAL", "return cjson.encode({a = 1})", "0"),
	testutil.S(":1", "EVAL", "return cjson.decode('{\"a\":1}').a", "0"),
	testutil.S(`"y"`, "EVAL", "return cjson.decode('[\"x\",\"y\"]')[2]", "0"),
	testutil.S(`"PONG"`, "EVAL", "return redis.pcall('ping').ok", "0"),
	// Script cache.
	testutil.S(`"`+sha1Return1+`"`, "SCRIPT", "LOAD", "return 1"),
	testutil.S(`*[:1, :0]`, "SCRIPT", "EXISTS", sha1Return1, sha1Unknown),
	testutil.S(":1", "EVALSHA", sha1Return1, "0"),
	testutil.S(":1", "EVALSHA", strings.ToUpper(sha1Return1), "0"),
	testutil.S(":1", "EVALSHA_RO", sha1Return1, "0"),
	testutil.S(noScript, "EVALSHA", sha1Unknown, "0"),
	testutil.S(noScript, "EVALSHA_RO", sha1Unknown, "0"),
	testutil.S(":1", "EVAL_RO", "return 1", "0"),
	testutil.S("+OK", "SCRIPT", "FLUSH"),
	testutil.S(`*[:0]`, "SCRIPT", "EXISTS", sha1Return1),
	testutil.S(noScript, "EVALSHA", sha1Return1, "0"),
	testutil.S("+OK", "SCRIPT", "FLUSH", "SYNC"),
	testutil.S("+OK", "SCRIPT", "FLUSH", "ASYNC"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try SCRIPT HELP.", "SCRIPT", "FOO"),
	testutil.S("-NOTBUSY No scripts in execution right now.", "SCRIPT", "KILL"),
	testutil.S(errNotInt, "EVAL", "return 1", "x"),
	testutil.S("-ERR Number of keys can't be negative", "EVAL", "return 1", "-1"),
	testutil.S("-ERR Number of keys can't be greater than number of args", "EVAL", "return 1", "2", "k"),
	testutil.S(arityErr("eval"), "EVAL", "return 1"),
	testutil.S(arityErr("evalsha"), "EVALSHA", sha1Return1),
}

// evalDataScript covers redis.call/pcall against real data.
var evalDataScript = []testutil.Step{
	testutil.S("+OK", "EVAL", "return redis.call('set', KEYS[1], ARGV[1])", "1", "k", "v"),
	testutil.S(`"v"`, "EVAL", "return redis.call('get', KEYS[1])", "1", "k"),
	testutil.S("(nil)", "EVAL", "return redis.call('get', 'missing')", "0"),
	testutil.S(":1", "EVAL", "return redis.call('incr', 'n')", "0"),
	testutil.S(`*[:2, :3]`, "EVAL", "return {redis.call('incr', 'n'), redis.call('incr', 'n')}", "0"),
	testutil.S(":4", "EVAL", "local n = redis.call('get', 'n'); return tonumber(n) + 1", "0"),
	testutil.S(":3", "EVAL", "return redis.call('rpush', 'l', 'a', 'b', 'c')", "0"),
	testutil.S(`*["a", "b", "c"]`, "EVAL", "return redis.call('lrange', 'l', 0, -1)", "0"),
	testutil.S(`"table"`, "EVAL", "return type(redis.pcall('incr', 'k'))", "0"),
	testutil.S(`"ERR value is not an integer or out of range"`, "EVAL", "return redis.pcall('incr', 'k').err", "0"),
	testutil.S(`"WRONGTYPE Operation against a key holding the wrong kind of value"`, "EVAL", "return redis.pcall('lpush', 'k', 'x').err", "0"),
	testutil.S(`"caught"`, "EVAL", "local ok, e = pcall(function() redis.call('incr', 'k') end); if not ok then return 'caught' end", "0"),
	testutil.S(`:1`, "EVAL", "redis.setresp(3); return redis.call('sismember', 'nosuch', 'a') + 1", "0"),
	testutil.S(`*["f", "v"]`, "EVAL", "redis.call('hset', 'h', 'f', 'v'); redis.setresp(3); return redis.call('hgetall', 'h')", "0"),
	testutil.S(`*["f", "v"]`, "EVAL", "redis.setresp(2); return redis.call('hgetall', 'h')", "0"),
	testutil.S(`"1.5"`, "EVAL", "redis.call('zadd', 'z', 1.5, 'a'); redis.setresp(3); return redis.call('zscore', 'z', 'a')", "0"),
	testutil.S(":1", "EVAL", "return redis.call('select', 1) and 1", "0"),
}

func TestScripting_Types(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), evalTypesScript)
}

func TestScripting_Data(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), evalDataScript)
}

func TestScriptingDifferential(t *testing.T) {
	testutil.Differential(t, concat(evalTypesScript, evalDataScript), testutil.KnownDivergences)
}

// errWithPrefix runs cmd and fails unless the reply is an error starting with
// prefix (Redis appends "script: <sha>, on @user_script:N." which varies).
func errWithPrefix(t *testing.T, c *testutil.Client, prefix string, cmd ...string) {
	t.Helper()
	testutil.RecordCommands(t)(cmd[0])
	got := c.DoS(cmd...)
	if !strings.HasPrefix(got, "-"+prefix) {
		t.Fatalf("%v: got %s want error prefix %q", cmd, got, prefix)
	}
}

func TestScripting_ErrorReplies(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S("+OK", "SET", "s", "abc")})
	errWithPrefix(t, c, "ERR value is not an integer or out of range", "EVAL", "return redis.call('incr', 's')", "0")
	errWithPrefix(t, c, "WRONGTYPE", "EVAL", "return redis.call('lpush', 's', 'x')", "0")
	errWithPrefix(t, c, "ERR Unknown Redis command called from script", "EVAL", "return redis.call('nosuchcmd')", "0")
	errWithPrefix(t, c, "ERR Please specify at least one argument for this redis lib call", "EVAL", "return redis.call()", "0")
	errWithPrefix(t, c, "ERR Write commands are not allowed from read-only scripts", "EVAL_RO", "return redis.call('set', 'k', 'v')", "0")
	errWithPrefix(t, c, "ERR Write commands are not allowed from read-only scripts", "EVALSHA_RO", loadScript(t, c, "return redis.call('set', 'k', 'v')"), "0")
	errWithPrefix(t, c, "ERR Error compiling script", "EVAL", "return +", "0")
	errWithPrefix(t, c, "ERR", "EVAL", "error('boom')", "0")
	errWithPrefix(t, c, "ERR This Redis command is not allowed from script", "EVAL", "return redis.call('subscribe', 'x')", "0")
	if got := c.DoS("EXISTS", "k"); got != ":0" {
		t.Fatalf("read-only script wrote: EXISTS got %s want :0", got)
	}
}

// loadScript loads code and returns its SHA1.
func loadScript(t *testing.T, c *testutil.Client, code string) string {
	t.Helper()
	v, err := c.Do("SCRIPT", "LOAD", code)
	if err != nil {
		t.Fatalf("SCRIPT LOAD: %v", err)
	}
	return string(v.Str)
}

func TestScripting_KillBusyScript(t *testing.T) {
	killBusyScript(t, func(c *config.Config) { c.BusyReplyThreshold.Store(100) })
}

// TestScripting_KillBusyScript_LuaTimeLimit: in Redis lua-time-limit is an
// alias of busy-reply-threshold, so setting it alone makes a script BUSY.
func TestScripting_KillBusyScript_LuaTimeLimit(t *testing.T) {
	killBusyScript(t, func(c *config.Config) { c.LuaTimeLimit.Store(100) })
}

// killBusyScript starts an infinite script, checks other clients get -BUSY
// and that SCRIPT KILL stops it.
func killBusyScript(t *testing.T, mutate func(c *config.Config)) {
	t.Helper()
	_, addr := testutil.StartServer(t, mutate)
	runner := testutil.MustDial(t, addr)
	other := testutil.MustDial(t, addr)
	if err := runner.Send("EVAL", "while true do end", "0"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := other.DoS("PING")
		if strings.HasPrefix(got, "-BUSY") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PING during busy script: got %s, want -BUSY ...", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := other.DoS("SCRIPT", "KILL"); got != "+OK" {
		t.Fatalf("SCRIPT KILL: got %s want +OK", got)
	}
	v, err := runner.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Render(v, false); !strings.HasPrefix(got, "-ERR Script killed by user with SCRIPT KILL") {
		t.Fatalf("killed script reply: got %s", got)
	}
	if got := other.DoS("PING"); got != "+PONG" {
		t.Fatalf("PING after kill: got %s want +PONG", got)
	}
}

// libCode registers one write function and one no-writes function.
const libCode = "#!lua name=lib\n" +
	"redis.register_function('f1', function(keys, args) return args[1] end)\n" +
	"redis.register_function{function_name='ro1', callback=function(keys, args) return redis.call('get', keys[1]) end, flags={'no-writes'}}\n"

const libListEntry = `*["library_name", "lib", "engine", "LUA", "functions", *[` +
	`*["name", "f1", "description", (nil), "flags", *[]], ` +
	`*["name", "ro1", "description", (nil), "flags", *[+no-writes]]]]`

var functionScript = []testutil.Step{
	testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode),
	testutil.S("-ERR Library 'lib' already exists", "FUNCTION", "LOAD", libCode),
	testutil.S(`"lib"`, "FUNCTION", "LOAD", "REPLACE", libCode),
	testutil.S("+OK", "SET", "k", "val"),
	testutil.S(`"hi"`, "FCALL", "f1", "0", "hi", "ignored"),
	testutil.S(`"val"`, "FCALL", "ro1", "1", "k"),
	testutil.S(`"val"`, "FCALL_RO", "ro1", "1", "k"),
	testutil.S("-ERR Can not execute a script with write flag using *_ro command.", "FCALL_RO", "f1", "0", "hi"),
	testutil.S("-ERR Function not found", "FCALL", "nosuch", "0"),
	testutil.S("-ERR Function not found", "FCALL_RO", "nosuch", "0"),
	testutil.S("-ERR Number of keys can't be greater than number of args", "FCALL", "f1", "3", "a"),
	testutil.S(`*[]`, "FUNCTION", "LIST", "LIBRARYNAME", "zzz*"),
	testutil.S(`*["running_script", (nil), "engines", *["LUA", *["libraries_count", :1, "functions_count", :2]]]`, "FUNCTION", "STATS"),
	testutil.S("-ERR Library not found", "FUNCTION", "DELETE", "nosuch"),
	testutil.S("+OK", "FUNCTION", "DELETE", "lib"),
	testutil.S("-ERR Function not found", "FCALL", "f1", "0"),
	testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode),
	testutil.S("+OK", "FUNCTION", "FLUSH"),
	testutil.S(`*[]`, "FUNCTION", "LIST"),
	testutil.S("-ERR Missing library metadata", "FUNCTION", "LOAD", "return 1"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try FUNCTION HELP.", "FUNCTION", "FOO"),
	testutil.S("-NOTBUSY No scripts in execution right now.", "FUNCTION", "KILL"),
}

func TestFunctionCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), functionScript)
}

func TestFunctionCommandsDifferential(t *testing.T) {
	testutil.Differential(t, functionScript, testutil.KnownDivergences)
}

func TestFunction_DumpRestore(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode)})
	v, err := c.Do("FUNCTION", "DUMP")
	if err != nil || len(v.Str) == 0 {
		t.Fatalf("FUNCTION DUMP: got %q, %v, want a payload", v.Str, err)
	}
	payload := string(v.Str)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "FUNCTION", "FLUSH"),
		testutil.S("-ERR Function not found", "FCALL", "f1", "0"),
		testutil.S("+OK", "FUNCTION", "RESTORE", payload),
		testutil.S(`"hi"`, "FCALL", "f1", "0", "hi"),
	})
	// candidate B23: RESTORE text may be unquoted in Redis; confirm in CI.
	if got := c.DoS("FUNCTION", "RESTORE", payload); !strings.HasPrefix(got, "-ERR Library") {
		t.Fatalf("FUNCTION RESTORE duplicate: got %s, want prefix -ERR Library", got)
	}
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "FUNCTION", "RESTORE", payload, "REPLACE"),
		testutil.S("+OK", "FUNCTION", "RESTORE", payload, "FLUSH"),
		testutil.S("-ERR Wrong restore policy given, value should be either FLUSH, APPEND or REPLACE.", "FUNCTION", "RESTORE", payload, "BOGUS"),
		testutil.S("-ERR payload version or checksum are wrong", "FUNCTION", "RESTORE", "garbage"),
	})
}

// A script may return more elements than a client aggregate may hold: the
// internal reply-conversion reader must not apply the client limit.
func TestScripting_ReplyLargerThanMultiBulkLimit(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	script := "for i = 1, 1100 do local t = {} for j = 1, 1000 do t[j] = 'x' end " +
		"redis.call('RPUSH', KEYS[1], unpack(t)) end return #redis.call('LRANGE', KEYS[1], 0, -1)"
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":1100000", "EVAL", script, "1", "big")})
}
