// Tests that hostile scripts driving deep recursion end in an ordinary EVAL
// error reply instead of a Go stack overflow that kills the server.
package server_test

import (
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestEval_HostileRecursion_ErrorsAndServerSurvives runs each recursion
// vector through a live server and checks that the server still answers PING
// and that the interpreter is still usable. Vectors with a non-empty want
// must fail with an error containing it; the others (deep but finite
// programs) may succeed or fail, as long as the process survives.
func TestEval_HostileRecursion_ErrorsAndServerSurvives(t *testing.T) {
	const deep = 1 << 20
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{"IndexTableLoop", "local t=setmetatable({},{}) getmetatable(t).__index=t return t.x", "loop in gettable"},
		{"NewindexTableLoop", "local t=setmetatable({},{}) getmetatable(t).__newindex=t t.x=1 return 1", "loop in settable"},
		{"IndexTwoTableLoop", "local a,b={},{} setmetatable(a,{__index=b}) setmetatable(b,{__index=a}) return a.x", "loop in gettable"},
		{"CallTableLoop", "local t=setmetatable({},{}) getmetatable(t).__call=t return t()", "attempt to call"},
		{"LuaRecursion", "local function f() return 1+f() end return f()", "stack overflow"},
		{"IndexFunctionRecursion", "local t t=setmetatable({},{__index=function(s,k) return t[k] end}) return t.x", "stack overflow"},
		{"ConcatRecursion", "local t t=setmetatable({},{__concat=function(a,b) return t..b end}) return t..'x'", "stack overflow"},
		{"EqRecursion", "local m={} m.__eq=function(a,b) return a==b end local a,b=setmetatable({},m),setmetatable({},m) return a==b", "stack overflow"},
		{"LtRecursion", "local m={} m.__lt=function(a,b) return a<b end local a,b=setmetatable({},m),setmetatable({},m) return a<b", "stack overflow"},
		{"LeRecursion", "local m={} m.__le=function(a,b) return a<=b end local a,b=setmetatable({},m),setmetatable({},m) return a<=b", "stack overflow"},
		{"TostringRecursion", "local t t=setmetatable({},{__tostring=function(s) return tostring(s) end}) return tostring(t)", "stack overflow"},
		{"PcallNesting", "local function f() return pcall(f) end f() error('x')", "user_script"},
		{"ErrorTostringRecursion", "error(setmetatable({},{__tostring=function(s) return tostring(s) end}))", ""},
		{"DeepParens", "return " + strings.Repeat("(", deep) + "1" + strings.Repeat(")", deep), ""},
		{"DeepTableConstructor", "return " + strings.Repeat("{", deep) + strings.Repeat("}", deep), ""},
		{"LongConcatChain", "local a='x' return #(a" + strings.Repeat("..a", deep) + ")", ""},
		{"DeepUnaryMinus", "return " + strings.Repeat("-", deep) + "1", ""},
		{"DeepNot", "return " + strings.Repeat("not ", deep) + "1", ""},
		{"DeepPower", "return 1" + strings.Repeat("^1", deep), ""},
		{"DeepBlocks", strings.Repeat("do ", deep) + strings.Repeat("end ", deep), ""},
		{"DeepFunctions", strings.Repeat("local function f() ", deep) + strings.Repeat("end ", deep), ""},
		{"DeepIfs", strings.Repeat("if 1 then ", deep) + strings.Repeat("end ", deep), ""},
		{"DeepIndexChain", "local t={} return t" + strings.Repeat("[1]", deep), ""},
		{"DeepCallArgs", "local f=print return " + strings.Repeat("type(", deep) + "1" + strings.Repeat(")", deep), ""},
		{"JSONEncodeSelfReference", "local t={} t[1]=t return cjson.encode(t)", ""},
		{"JSONEncodeDeep", "local t={} local c=t for i=1,100000 do c[1]={} c=c[1] end return cjson.encode(t)", ""},
		{"JSONDecodeDeepArray", "return cjson.decode(string.rep('[', 1000000))", ""},
		{"JSONDecodeDeepObject", "return cjson.decode(string.rep('{\"a\":', 1000000))", ""},
		{"MsgpackEncodeSelfReference", "local t={} t[1]=t return cmsgpack.pack(t)", ""},
		{"MsgpackDecodeDeep", "return cmsgpack.unpack(string.rep('\\145', 1000000))", ""},
		{"PatternOptionalChain", "return string.find(string.rep('a', 100000), string.rep('a?', 100000))", ""},
		{"PatternLongSubject", "return string.match(string.rep('a', 1000000), '(.-)%1$')", ""},
		{"GsubRecursion", "local function f(s) return (string.gsub(s, '.', f)) end return f('ab')", "stack overflow"},
		{"SortComparatorRecursion", "local function c(a,b) table.sort({3,2,1}, c) return a<b end table.sort({3,2,1}, c) return 1", "stack overflow"},
	}
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.DoS("EVAL", tc.script, "0")
			if tc.want != "" && (!strings.HasPrefix(got, "-") || !strings.Contains(got, tc.want)) {
				t.Errorf("EVAL reply = %.200s, want error containing %q", got, tc.want)
			}
			if got := c.DoS("PING"); got != "+PONG" {
				t.Fatalf("PING after hostile script = %s, want +PONG", got)
			}
			if got := c.DoS("EVAL", "return redis.call('set', KEYS[1], 'v')", "1", "k"); got != "+OK" {
				t.Fatalf("EVAL after hostile script = %s, want +OK", got)
			}
		})
	}
}

// TestEval_LongButBoundedExpressions_StillRun checks that the nesting limits
// leave room for long ordinary expressions.
func TestEval_LongButBoundedExpressions_StillRun(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	tests := []struct{ name, script, want string }{
		{"AddChain", "return 1" + strings.Repeat("+1", 5000), ":5001"},
		{"ConcatChain", "local a='x' return #(a" + strings.Repeat("..a", 150) + ")", ":151"},
		{"NestedParens", "return " + strings.Repeat("(", 150) + "7" + strings.Repeat(")", 150), ":7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.DoS("EVAL", tc.script, "0"); got != tc.want {
				t.Errorf("EVAL = %.200s, want %s", got, tc.want)
			}
		})
	}
}
