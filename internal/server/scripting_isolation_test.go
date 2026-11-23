// Tests that a script cannot change globals, library tables or metatables of
// a pooled interpreter in a way that later scripts from other clients see.
package server_test

import (
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestEval_GlobalMutation_NotVisibleToLaterScripts runs each mutation route
// as script A, then checks with script B on the same and on another
// connection that the original behaviour is intact.
func TestEval_GlobalMutation_NotVisibleToLaterScripts(t *testing.T) {
	const pwn = "function() return 'pwned' end"
	tests := []struct {
		name    string
		attack  string
		observe string
		want    string
	}{
		{"RawsetGlobalFunction", "rawset(_G,'tostring'," + pwn + ")", "return tostring(5)", `"5"`},
		{"RawsetNewGlobal", "rawset(_G,'zz',41)", "return type(rawget(_G,'zz'))", `"nil"`},
		{"AssignExistingGlobal", "tostring=" + pwn, "return tostring(5)", `"5"`},
		{"AssignViaG", "_G.type=" + pwn, "return type(5)", `"number"`},
		{"SetmetatableGlobals", "setmetatable(_G,{__index=function() return 1 end})", "return type(getmetatable(_G))", `"nil"`},
		{"StringLibField", "string.rep=" + pwn, "return string.rep('x',2)", `"xx"`},
		{"RawsetStringLib", "rawset(string,'rep'," + pwn + ")", "return ('x'):rep(2)", `"xx"`},
		{"StringMetatableIndex", "getmetatable('').__index=" + "{rep=" + pwn + "}", "return ('x'):rep(2)", `"xx"`},
		{"TableLibField", "rawset(table,'concat'," + pwn + ")", "return table.concat({'a','b'})", `"ab"`},
		{"RedisCall", "redis.call=" + pwn, "return redis.call('ping')", "+PONG"},
		{"RawsetRedis", "rawset(redis,'call'," + pwn + ")", "return redis.call('ping')", "+PONG"},
		{"SetmetatableLib", "setmetatable(math,{__index=function() return 1 end})", "return type(math.nope)", `"nil"`},
		{"ReplaceCjson", "rawset(_G,'cjson',{encode=" + pwn + "})", "return cjson.encode({1})", `"[1]"`},
	}
	_, addr := testutil.StartServer(t, nil)
	a := testutil.MustDial(t, addr)
	b := testutil.MustDial(t, addr)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for range 5 {
				if got := a.DoS("EVAL", tc.attack+" return 1", "0"); !strings.HasPrefix(got, "-") {
					t.Errorf("attack = %s, want an error reply", got)
				}
				for _, c := range []*testutil.Client{a, b} {
					if got := c.DoS("EVAL", tc.observe, "0"); got != tc.want {
						t.Fatalf("later script = %s, want %s", got, tc.want)
					}
				}
			}
		})
	}
}

// TestFunction_GlobalMutation_NotVisibleToEval checks that library code run
// by FUNCTION LOAD or FCALL cannot alter the globals that EVAL scripts see,
// since functions and EVAL share pooled interpreters.
func TestFunction_GlobalMutation_NotVisibleToEval(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	a := testutil.MustDial(t, addr)
	b := testutil.MustDial(t, addr)
	evil := "#!lua name=evil\nrawset(_G,'tostring',function() return 'pwned' end)\nredis.register_function('f', function() return 1 end)"
	if got := a.DoS("FUNCTION", "LOAD", evil); !strings.HasPrefix(got, "-") {
		t.Errorf("FUNCTION LOAD of mutating library = %s, want an error reply", got)
	}
	lib := "#!lua name=lib\nredis.register_function('g', function() rawset(_G,'type',function() return 'pwned' end) return 1 end)"
	if got := a.DoS("FUNCTION", "LOAD", lib); got != `"lib"` {
		t.Fatalf("FUNCTION LOAD = %s, want \"lib\"", got)
	}
	for range 5 {
		if got := a.DoS("FCALL", "g", "0"); !strings.HasPrefix(got, "-") {
			t.Errorf("FCALL g = %s, want an error reply", got)
		}
		for _, c := range []*testutil.Client{a, b} {
			if got := c.DoS("EVAL", "return tostring(5)..type(5)", "0"); got != `"5number"` {
				t.Fatalf("EVAL after function mutation = %s, want \"5number\"", got)
			}
		}
	}
}
