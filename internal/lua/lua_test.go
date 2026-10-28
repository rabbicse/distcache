package lua

import (
	"strings"
	"testing"
)

func run(t *testing.T, src string) []Value {
	t.Helper()
	L := NewState()
	cl, err := L.Compile(src, "user_script")
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	rets, err := L.PCall(cl)
	if err != nil {
		t.Fatalf("run %q: %v", src, err)
	}
	return rets
}

func runErr(src string) error {
	L := NewState()
	L.ProtectGlobals = true
	cl, err := L.Compile(src, "user_script")
	if err != nil {
		return err
	}
	_, err = L.PCall(cl)
	return err
}

func TestLuaBasics(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"return 1 + 2 * 3", "7"},
		{"return 2 ^ 10", "1024"},
		{"return -2 ^ 2", "-4"},
		{"return 7 % 3, -7 % 3", "1,2"},
		{"return 10 / 4", "2.5"},
		{"return 'a' .. 'b' .. 1", "ab1"},
		{"return #'hello'", "5"},
		{"return 1 < 2, 'a' < 'b', 2 <= 2, 3 > 4", "true,true,true,false"},
		{"return nil or 'x', false and 1, 1 and 2", "x,false,2"},
		{"local t = {1,2,3} return #t", "3"},
		{"local t = {} t[1]=1 t[2]=2 t.x='y' return #t, t.x", "2,y"},
		{"local s = 0 for i=1,10 do s = s + i end return s", "55"},
		{"local s = 0 for i=10,1,-2 do s = s + i end return s", "30"},
		{"local t = {a=1,b=2} local n=0 for k,v in pairs(t) do n=n+v end return n", "3"},
		{"local t = {5,6,7} local n=0 for i,v in ipairs(t) do n=n+i*v end return n", "38"},
		{"local function f(n) if n <= 1 then return 1 end return n * f(n-1) end return f(10)", "3628800"},
		{"local function f(...) return select('#', ...) end return f(1, nil, 3)", "3"},
		{"local function f(...) local a, b = ... return b end return f(1, 2, 3)", "2"},
		{"local function mk() local c = 0 return function() c = c + 1 return c end end local f = mk() f() f() return f()", "3"},
		{"local fs = {} for i=1,3 do fs[i] = function() return i end end return fs[1]() + fs[2]() + fs[3]()", "6"},
		{"local x = 1 do local x = 2 end return x", "1"},
		{"local i = 0 while true do i = i + 1 if i == 5 then break end end return i", "5"},
		{"local i = 0 repeat local j = i i = i + 1 until j >= 3 return i", "4"},
		{"return tostring(nil), tostring(true), tonumber('0x10'), tonumber('  12  '), tonumber('z', 36)", "nil,true,16,12,35"},
		{"return math.floor(3.7), math.max(1, 5, 3), math.huge > 0", "3,5,true"},
		{"return string.format('%d %s %5.2f %x %q', 42, 'hi', 3.14159, 255, 'a\"b')", "42 hi  3.14 ff \"a\\\"b\""},
		{"return string.format('%g %g', 0.1, 1e20)", "0.1 1e+20"},
		{"return ('hello'):upper(), string.rep('ab', 3), string.sub('hello', 2, -2)", "HELLO,ababab,ell"},
		{"return string.find('hello world', 'o w'), string.find('hello', 'l+')", "5,3,4"},
		{"return string.match('key:123', '(%a+):(%d+)')", "key,123"},
		{"return string.gsub('hello world', 'o', '0')", "hell0 w0rld,2"},
		{"return string.gsub('abc', '%w', '%0%0')", "aabbcc,3"},
		{"return (string.gsub('hello', '(l)(l)', function(a, b) return b:upper() .. a end))", "heLlo"},
		{"local t = {} for w in string.gmatch('one two three', '%a+') do t[#t+1] = w end return table.concat(t, ',')", "one,two,three"},
		{"return string.byte('A'), string.char(72, 105)", "65,Hi"},
		{"local t = {3,1,2} table.sort(t) return table.concat(t, ' ')", "1 2 3"},
		{"local t = {3,1,2} table.sort(t, function(a,b) return a > b end) return table.concat(t, ' ')", "3 2 1"},
		{"local t = {1,2} table.insert(t, 3) table.insert(t, 1, 0) return table.concat(t, ',')", "0,1,2,3"},
		{"local t = {1,2,3} local v = table.remove(t, 1) return v, #t", "1,2"},
		{"return unpack({1, 2, 3})", "1,2,3"},
		{"return pcall(function() error('boom') end)", "false,user_script:1: boom"},
		{"return pcall(function() error({x=1}) end) == false", "true"},
		{"local ok, e = pcall(function() local t = nil; return t.x end) return e", "user_script:1: attempt to index local 't' (a nil value)"},
		{"return bit.band(0xff, 0x0f), bit.bor(1, 2), bit.bxor(3, 1), bit.lshift(1, 4), bit.tohex(255)", "15,3,2,16,000000ff"},
		{"return cjson.encode({1, 2, 3}), cjson.encode({a = 'x'})", "[1,2,3],{\"a\":\"x\"}"},
		{"local t = cjson.decode('{\"a\":[1,2,{\"b\":true}]}') return t.a[3].b, #t.a", "true,3"},
		{"local p = cmsgpack.pack({1, 'two', 3.5}) local t = cmsgpack.unpack(p) return t[2], t[3]", "two,3.5"},
		{"local t = setmetatable({}, {__index = function(t, k) return k .. '!' end}) return t.hi", "hi!"},
		{"local mt = {__add = function(a, b) return a.v + b.v end} local a = setmetatable({v=1}, mt) local b = setmetatable({v=2}, mt) return a + b", "3"},
		{"-- comment\nlocal s = [[long\nstring]] --[[ block\ncomment ]] return s", "long\nstring"},
		{"local a, b, c = (function() return 1, 2, 3 end)() return a + b + c", "6"},
		{"local t = {(function() return 1, 2 end)()} return #t", "2"},
		{"local t = {(function() return 1, 2 end)(), 5} return #t", "2"},
		{"return 1e3, 0.5, 3.0, 1/0, -1/0", "1000,0.5,3,inf,-inf"},
		{"return select(2, 'a', 'b', 'c')", "b,c"},
		{"local o = {n=0} function o:inc(k) self.n = self.n + k return self end o:inc(2):inc(3) return o.n", "5"},
		{"local t = {} t.a = {} t.a.b = function() return 'deep' end return t.a.b()", "deep"},
		{"return #{n=1}, #{1, 2, nil, 4} >= 2", "0,true"},
		{"return 10 == 10.0, 'a' == 'a', {} == {}", "true,true,false"},
		{"return math.fmod(7, 3), math.abs(-2)", "1,2"},
		{"return string.format('%5s|%-5s|', 'ab', 'cd')", "   ab|cd   |"},
		{"return string.len('\\65\\066'), '\\n' == '\\10'", "2,true"},
	}
	for _, c := range cases {
		rets := run(t, c.src)
		parts := make([]string, len(rets))
		for i, r := range rets {
			parts[i] = ToString(r)
		}
		if got := strings.Join(parts, ","); got != c.want {
			t.Errorf("%s\n got: %q\nwant: %q", c.src, got, c.want)
		}
	}
}

func TestLuaErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"x = 1", "Script attempted to create global variable 'x'"},
		{"return y", "Script attempted to access nonexistent global variable 'y'"},
		{"local t = nil; t.x = 1", "attempt to index local 't' (a nil value)"},
		{"return 1 + {}", "attempt to perform arithmetic on a table value"},
		{"return {} < {}", "attempt to compare two table values"},
		{"return #nil", "attempt to get length of a nil value"},
		{"local f; f()", "attempt to call local 'f' (a nil value)"},
		{"return 'a' .. nil", "attempt to concatenate a nil value"},
		{"if then end", "unexpected symbol"},
		{"return (", "unexpected symbol near '<eof>'"},
		{"break", "no loop to break"},
		{"local function f() return f() end return f()", "stack overflow"},
	}
	for _, c := range cases {
		err := runErr(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want containing %q", c.src, err, c.want)
		}
	}
}

func TestLuaHookAbort(t *testing.T) {
	L := NewState()
	L.HookEvery = 100
	n := 0
	L.Hook = func() error {
		n++
		if n > 5 {
			return &FatalError{Msg: "killed"}
		}
		return nil
	}
	cl, err := L.Compile("local ok = pcall(function() while true do end end) return ok", "user_script")
	if err != nil {
		t.Fatal(err)
	}
	_, err = L.PCall(cl)
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("expected fatal kill error, got %v", err)
	}
}
