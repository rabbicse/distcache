package lua

import (
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
)

func arg(args []Value, i int) Value {
	if i < len(args) {
		return args[i]
	}
	return nil
}

func (L *State) checkTable(args []Value, i int, fname string) *Table {
	t, ok := arg(args, i).(*Table)
	if !ok {
		L.ArgError(i+1, fname, "table expected, got "+typeNameOrNoValue(args, i))
	}
	return t
}

func typeNameOrNoValue(args []Value, i int) string {
	if i >= len(args) {
		return "no value"
	}
	return TypeName(args[i])
}

func (L *State) checkNumber(args []Value, i int, fname string) float64 {
	n, ok := ToNumber(arg(args, i))
	if !ok {
		L.ArgError(i+1, fname, "number expected, got "+typeNameOrNoValue(args, i))
	}
	return n
}

func (L *State) checkInt(args []Value, i int, fname string) int {
	return int(L.checkNumber(args, i, fname))
}

func (L *State) optInt(args []Value, i int, fname string, def int) int {
	if arg(args, i) == nil {
		return def
	}
	return L.checkInt(args, i, fname)
}

func (L *State) checkString(args []Value, i int, fname string) string {
	s, ok := ToStringCoerce(arg(args, i))
	if !ok {
		L.ArgError(i+1, fname, "string expected, got "+typeNameOrNoValue(args, i))
	}
	return s
}

func one(v Value) []Value { return []Value{v} }

func openBase(L *State) {
	g := L.Globals
	g.RawSet("_G", g)
	g.RawSet("_VERSION", "Lua 5.1")
	L.Register("assert", func(L *State, a []Value) []Value {
		if len(a) == 0 {
			L.ArgError(1, "assert", "value expected")
		}
		if !Truthy(a[0]) {
			if len(a) > 1 {
				L.Error(a[1])
			}
			L.RuntimeError("assertion failed!")
		}
		return a
	})
	L.Register("error", func(L *State, a []Value) []Value {
		v := arg(a, 0)
		level := 1
		if len(a) > 1 {
			level = int(L.checkNumber(a, 1, "error"))
		}
		if s, ok := v.(string); ok && level > 0 {
			v = L.Chunk + ":" + strconv.Itoa(L.line) + ": " + s
		}
		L.Error(v)
		return nil
	})
	L.Register("type", func(L *State, a []Value) []Value {
		if len(a) == 0 {
			L.ArgError(1, "type", "value expected")
		}
		return one(TypeName(a[0]))
	})
	L.Register("tostring", func(L *State, a []Value) []Value { return one(L.ToStringMeta(arg(a, 0))) })
	L.Register("tonumber", func(L *State, a []Value) []Value {
		base := 10
		if len(a) > 1 && a[1] != nil {
			base = L.checkInt(a, 1, "tonumber")
		}
		if base == 10 {
			if n, ok := ToNumber(arg(a, 0)); ok {
				return one(n)
			}
			return one(nil)
		}
		s, ok := ToStringCoerce(arg(a, 0))
		if !ok {
			return one(nil)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(strings.ToLower(s)), base, 64)
		if err != nil {
			return one(nil)
		}
		return one(float64(n))
	})
	L.Register("rawget", func(L *State, a []Value) []Value {
		return one(L.checkTable(a, 0, "rawget").RawGet(arg(a, 1)))
	})
	L.Register("rawset", func(L *State, a []Value) []Value {
		t := L.checkTable(a, 0, "rawset")
		if t.ReadOnly {
			L.RuntimeError("Attempt to modify a readonly table")
		}
		t.RawSet(arg(a, 1), arg(a, 2))
		return one(t)
	})
	L.Register("rawequal", func(L *State, a []Value) []Value { return one(rawEqual(arg(a, 0), arg(a, 1))) })
	L.Register("setmetatable", func(L *State, a []Value) []Value {
		t := L.checkTable(a, 0, "setmetatable")
		if t.ReadOnly {
			L.RuntimeError("Attempt to modify a readonly table")
		}
		switch m := arg(a, 1).(type) {
		case nil:
			t.Meta = nil
		case *Table:
			t.Meta = m
		default:
			L.ArgError(2, "setmetatable", "nil or table expected")
		}
		return one(t)
	})
	L.Register("getmetatable", func(L *State, a []Value) []Value {
		if t, ok := arg(a, 0).(*Table); ok && t.Meta != nil {
			if p := t.Meta.RawGet("__metatable"); p != nil {
				return one(p)
			}
			return one(t.Meta)
		}
		if _, ok := arg(a, 0).(string); ok {
			return one(nil)
		}
		return one(nil)
	})
	next := &GoFunction{Name: "next", Fn: func(L *State, a []Value) []Value {
		t := L.checkTable(a, 0, "next")
		k, v, ok := t.Next(arg(a, 1))
		if !ok {
			L.RuntimeError("invalid key to 'next'")
		}
		if k == nil {
			return one(nil)
		}
		return []Value{k, v}
	}}
	g.RawSet("next", next)
	L.Register("pairs", func(L *State, a []Value) []Value {
		t := L.checkTable(a, 0, "pairs")
		return []Value{next, t, nil}
	})
	ipairsIter := &GoFunction{Name: "ipairs_iter", Fn: func(L *State, a []Value) []Value {
		t := a[0].(*Table)
		i := a[1].(float64) + 1
		v := t.RawGet(i)
		if v == nil {
			return one(nil)
		}
		return []Value{i, v}
	}}
	L.Register("ipairs", func(L *State, a []Value) []Value {
		t := L.checkTable(a, 0, "ipairs")
		return []Value{ipairsIter, t, float64(0)}
	})
	L.Register("unpack", luaUnpack)
	L.Register("select", func(L *State, a []Value) []Value {
		if s, ok := arg(a, 0).(string); ok && s == "#" {
			return one(float64(len(a) - 1))
		}
		n := L.checkInt(a, 0, "select")
		if n < 0 {
			n = len(a) + n
		} else if n == 0 {
			L.ArgError(1, "select", "index out of range")
		}
		if n >= len(a) {
			return nil
		}
		if n < 1 {
			L.ArgError(1, "select", "index out of range")
		}
		return a[n:]
	})
	L.Register("pcall", func(L *State, a []Value) []Value {
		if len(a) == 0 {
			L.ArgError(1, "pcall", "value expected")
		}
		rets, err := L.protectedCall(a[0], a[1:])
		if err != nil {
			return []Value{false, err.Value}
		}
		return append([]Value{true}, rets...)
	})
	L.Register("xpcall", func(L *State, a []Value) []Value {
		rets, err := L.protectedCall(arg(a, 0), nil)
		if err != nil {
			h := L.Call(arg(a, 1), []Value{err.Value})
			return append([]Value{false}, h...)
		}
		return append([]Value{true}, rets...)
	})
	L.Register("print", func(L *State, a []Value) []Value { return nil })
	L.Register("collectgarbage", func(L *State, a []Value) []Value { return one(float64(0)) })
	L.Register("gcinfo", func(L *State, a []Value) []Value { return one(float64(0)) })
}

// protectedCall runs fn catching Lua (but not fatal) errors.
func (L *State) protectedCall(fn Value, args []Value) (rets []Value, lerr *LuaError) {
	depth := L.depth
	line := L.line
	defer func() {
		if r := recover(); r != nil {
			L.depth = depth
			L.line = line
			if e, ok := r.(*LuaError); ok {
				lerr = e
				return
			}
			panic(r)
		}
	}()
	return L.call(fn, args, "?"), nil
}

func luaUnpack(L *State, a []Value) []Value {
	t := L.checkTable(a, 0, "unpack")
	i := L.optInt(a, 1, "unpack", 1)
	j := t.Len()
	if arg(a, 2) != nil {
		j = L.checkInt(a, 2, "unpack")
	}
	if i > j {
		return nil
	}
	if j-i >= 8000 {
		L.RuntimeError("too many results to unpack")
	}
	out := make([]Value, 0, j-i+1)
	for k := i; k <= j; k++ {
		out = append(out, t.RawGet(float64(k)))
	}
	return out
}

func openTable(L *State) {
	t := NewTable(0, 8)
	reg := func(name string, fn func(L *State, a []Value) []Value) {
		t.RawSet(name, &GoFunction{Name: name, Fn: fn})
	}
	reg("insert", func(L *State, a []Value) []Value {
		tb := L.checkTable(a, 0, "insert")
		n := tb.Len()
		switch len(a) {
		case 2:
			tb.RawSet(float64(n+1), a[1])
		case 3:
			pos := L.checkInt(a, 1, "insert")
			if pos > n+1 {
				n = pos - 1
			}
			for i := n; i >= pos; i-- {
				tb.RawSet(float64(i+1), tb.RawGet(float64(i)))
			}
			tb.RawSet(float64(pos), a[2])
		default:
			L.RuntimeError("wrong number of arguments to 'insert'")
		}
		return nil
	})
	reg("remove", func(L *State, a []Value) []Value {
		tb := L.checkTable(a, 0, "remove")
		n := tb.Len()
		pos := n
		if len(a) > 1 {
			pos = L.checkInt(a, 1, "remove")
		}
		if n == 0 {
			return nil
		}
		v := tb.RawGet(float64(pos))
		for i := pos; i < n; i++ {
			tb.RawSet(float64(i), tb.RawGet(float64(i+1)))
		}
		tb.RawSet(float64(n), nil)
		return one(v)
	})
	reg("concat", func(L *State, a []Value) []Value {
		tb := L.checkTable(a, 0, "concat")
		sep := ""
		if s, ok := arg(a, 1).(string); ok {
			sep = s
		}
		i := L.optInt(a, 2, "concat", 1)
		j := tb.Len()
		if arg(a, 3) != nil {
			j = L.checkInt(a, 3, "concat")
		}
		var b strings.Builder
		for k := i; k <= j; k++ {
			s, ok := ToStringCoerce(tb.RawGet(float64(k)))
			if !ok {
				L.RuntimeError("invalid value (at index %d) in table for 'concat'", k)
			}
			b.WriteString(s)
			if k < j {
				b.WriteString(sep)
			}
		}
		return one(b.String())
	})
	reg("getn", func(L *State, a []Value) []Value { return one(float64(L.checkTable(a, 0, "getn").Len())) })
	reg("maxn", func(L *State, a []Value) []Value {
		tb := L.checkTable(a, 0, "maxn")
		m := 0.0
		var k Value
		for {
			nk, _, _ := tb.Next(k)
			if nk == nil {
				break
			}
			if f, ok := nk.(float64); ok && f > m {
				m = f
			}
			k = nk
		}
		return one(m)
	})
	reg("sort", func(L *State, a []Value) []Value {
		tb := L.checkTable(a, 0, "sort")
		n := tb.Len()
		vals := make([]Value, n)
		for i := 0; i < n; i++ {
			vals[i] = tb.RawGet(float64(i + 1))
		}
		cmp := arg(a, 1)
		less := func(x, y Value) bool {
			if cmp != nil {
				r := L.Call(cmp, []Value{x, y})
				return len(r) > 0 && Truthy(r[0])
			}
			return L.lessThan(x, y)
		}
		sort.SliceStable(vals, func(i, j int) bool { return less(vals[i], vals[j]) })
		for i, v := range vals {
			tb.RawSet(float64(i+1), v)
		}
		return nil
	})
	reg("unpack", luaUnpack)
	L.Globals.RawSet("table", t)
}

func openMath(L *State) {
	m := NewTable(0, 32)
	reg := func(name string, fn func(L *State, a []Value) []Value) {
		m.RawSet(name, &GoFunction{Name: name, Fn: fn})
	}
	f1 := func(name string, fn func(float64) float64) {
		reg(name, func(L *State, a []Value) []Value { return one(fn(L.checkNumber(a, 0, name))) })
	}
	f1("abs", math.Abs)
	f1("ceil", math.Ceil)
	f1("floor", math.Floor)
	f1("sqrt", math.Sqrt)
	f1("sin", math.Sin)
	f1("cos", math.Cos)
	f1("tan", math.Tan)
	f1("asin", math.Asin)
	f1("acos", math.Acos)
	f1("atan", math.Atan)
	f1("exp", math.Exp)
	f1("log10", math.Log10)
	f1("sinh", math.Sinh)
	f1("cosh", math.Cosh)
	f1("tanh", math.Tanh)
	f1("deg", func(x float64) float64 { return x * 180 / math.Pi })
	f1("rad", func(x float64) float64 { return x * math.Pi / 180 })
	reg("log", func(L *State, a []Value) []Value {
		x := L.checkNumber(a, 0, "log")
		if len(a) > 1 {
			return one(math.Log(x) / math.Log(L.checkNumber(a, 1, "log")))
		}
		return one(math.Log(x))
	})
	reg("atan2", func(L *State, a []Value) []Value {
		return one(math.Atan2(L.checkNumber(a, 0, "atan2"), L.checkNumber(a, 1, "atan2")))
	})
	reg("pow", func(L *State, a []Value) []Value {
		return one(math.Pow(L.checkNumber(a, 0, "pow"), L.checkNumber(a, 1, "pow")))
	})
	reg("fmod", func(L *State, a []Value) []Value {
		return one(math.Mod(L.checkNumber(a, 0, "fmod"), L.checkNumber(a, 1, "fmod")))
	})
	reg("modf", func(L *State, a []Value) []Value {
		i, f := math.Modf(L.checkNumber(a, 0, "modf"))
		return []Value{i, f}
	})
	reg("frexp", func(L *State, a []Value) []Value {
		f, e := math.Frexp(L.checkNumber(a, 0, "frexp"))
		return []Value{f, float64(e)}
	})
	reg("ldexp", func(L *State, a []Value) []Value {
		return one(math.Ldexp(L.checkNumber(a, 0, "ldexp"), L.checkInt(a, 1, "ldexp")))
	})
	reg("max", func(L *State, a []Value) []Value {
		r := L.checkNumber(a, 0, "max")
		for i := 1; i < len(a); i++ {
			if v := L.checkNumber(a, i, "max"); v > r {
				r = v
			}
		}
		return one(r)
	})
	reg("min", func(L *State, a []Value) []Value {
		r := L.checkNumber(a, 0, "min")
		for i := 1; i < len(a); i++ {
			if v := L.checkNumber(a, i, "min"); v < r {
				r = v
			}
		}
		return one(r)
	})
	// Redis seeds the PRNG deterministically for every script.
	rng := rand.New(rand.NewSource(0))
	reg("random", func(L *State, a []Value) []Value {
		r := rng.Float64()
		switch len(a) {
		case 0:
			return one(r)
		case 1:
			u := L.checkNumber(a, 0, "random")
			if u < 1 {
				L.ArgError(1, "random", "interval is empty")
			}
			return one(math.Floor(r*u) + 1)
		default:
			lo := L.checkNumber(a, 0, "random")
			hi := L.checkNumber(a, 1, "random")
			if lo > hi {
				L.ArgError(2, "random", "interval is empty")
			}
			return one(math.Floor(r*(hi-lo+1)) + lo)
		}
	})
	reg("randomseed", func(L *State, a []Value) []Value {
		rng.Seed(int64(L.checkNumber(a, 0, "randomseed")))
		return nil
	})
	m.RawSet("pi", math.Pi)
	m.RawSet("huge", math.Inf(1))
	L.Globals.RawSet("math", m)
}

func openBit(L *State) {
	b := NewTable(0, 12)
	tobit := func(v float64) int32 { return int32(uint32(int64(v))) }
	reg := func(name string, fn func(L *State, a []Value) []Value) {
		b.RawSet(name, &GoFunction{Name: name, Fn: fn})
	}
	reg("tobit", func(L *State, a []Value) []Value { return one(float64(tobit(L.checkNumber(a, 0, "tobit")))) })
	reg("bnot", func(L *State, a []Value) []Value { return one(float64(^tobit(L.checkNumber(a, 0, "bnot")))) })
	nary := func(name string, op func(x, y int32) int32) {
		reg(name, func(L *State, a []Value) []Value {
			r := tobit(L.checkNumber(a, 0, name))
			for i := 1; i < len(a); i++ {
				r = op(r, tobit(L.checkNumber(a, i, name)))
			}
			return one(float64(r))
		})
	}
	nary("band", func(x, y int32) int32 { return x & y })
	nary("bor", func(x, y int32) int32 { return x | y })
	nary("bxor", func(x, y int32) int32 { return x ^ y })
	shift := func(name string, op func(x int32, n uint) int32) {
		reg(name, func(L *State, a []Value) []Value {
			x := tobit(L.checkNumber(a, 0, name))
			n := uint(tobit(L.checkNumber(a, 1, name))) & 31
			return one(float64(op(x, n)))
		})
	}
	shift("lshift", func(x int32, n uint) int32 { return int32(uint32(x) << n) })
	shift("rshift", func(x int32, n uint) int32 { return int32(uint32(x) >> n) })
	shift("arshift", func(x int32, n uint) int32 { return x >> n })
	shift("rol", func(x int32, n uint) int32 { u := uint32(x); return int32(u<<n | u>>(32-n)) })
	shift("ror", func(x int32, n uint) int32 { u := uint32(x); return int32(u>>n | u<<(32-n)) })
	reg("bswap", func(L *State, a []Value) []Value {
		u := uint32(tobit(L.checkNumber(a, 0, "bswap")))
		return one(float64(int32(u>>24 | (u>>8)&0xff00 | (u<<8)&0xff0000 | u<<24)))
	})
	reg("tohex", func(L *State, a []Value) []Value {
		x := uint32(tobit(L.checkNumber(a, 0, "tohex")))
		n := 8
		if len(a) > 1 {
			n = L.checkInt(a, 1, "tohex")
		}
		upper := n < 0
		if upper {
			n = -n
		}
		if n > 8 {
			n = 8
		}
		s := strconv.FormatUint(uint64(x), 16)
		for len(s) < 8 {
			s = "0" + s
		}
		s = s[8-n:]
		if upper {
			s = strings.ToUpper(s)
		}
		return one(s)
	})
	L.Globals.RawSet("bit", b)
}
