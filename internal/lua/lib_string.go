package lua

import (
	"fmt"
	"strconv"
	"strings"
)

func posrelat(pos, l int) int {
	if pos < 0 {
		pos += l + 1
	}
	if pos < 0 {
		return 0
	}
	return pos
}

func openString(L *State) {
	s := NewTable(0, 16)
	reg := func(name string, fn func(L *State, a []Value) []Value) {
		s.RawSet(name, &GoFunction{Name: name, Fn: fn})
	}
	reg("len", func(L *State, a []Value) []Value { return one(float64(len(L.checkString(a, 0, "len")))) })
	reg("lower", func(L *State, a []Value) []Value { return one(strings.ToLower(L.checkString(a, 0, "lower"))) })
	reg("upper", func(L *State, a []Value) []Value { return one(strings.ToUpper(L.checkString(a, 0, "upper"))) })
	reg("rep", func(L *State, a []Value) []Value {
		str := L.checkString(a, 0, "rep")
		n := L.checkInt(a, 1, "rep")
		if n <= 0 {
			return one("")
		}
		if len(str)*n > 512*1024*1024 {
			L.RuntimeError("resulting string too large")
		}
		return one(strings.Repeat(str, n))
	})
	reg("reverse", func(L *State, a []Value) []Value {
		b := []byte(L.checkString(a, 0, "reverse"))
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		return one(string(b))
	})
	reg("sub", func(L *State, a []Value) []Value {
		str := L.checkString(a, 0, "sub")
		l := len(str)
		i := posrelat(L.optInt(a, 1, "sub", 1), l)
		j := posrelat(L.optInt(a, 2, "sub", -1), l)
		if i < 1 {
			i = 1
		}
		if j > l {
			j = l
		}
		if i > j {
			return one("")
		}
		return one(str[i-1 : j])
	})
	reg("byte", func(L *State, a []Value) []Value {
		str := L.checkString(a, 0, "byte")
		l := len(str)
		i := posrelat(L.optInt(a, 1, "byte", 1), l)
		j := posrelat(L.optInt(a, 2, "byte", i), l)
		if i <= 0 {
			i = 1
		}
		if j > l {
			j = l
		}
		var out []Value
		for k := i; k <= j; k++ {
			out = append(out, float64(str[k-1]))
		}
		return out
	})
	reg("char", func(L *State, a []Value) []Value {
		b := make([]byte, len(a))
		for i := range a {
			c := L.checkInt(a, i, "char")
			if c < 0 || c > 255 {
				L.ArgError(i+1, "char", "invalid value")
			}
			b[i] = byte(c)
		}
		return one(string(b))
	})
	reg("format", strFormat)
	reg("find", func(L *State, a []Value) []Value { return strFindAux(L, a, true) })
	reg("match", func(L *State, a []Value) []Value { return strFindAux(L, a, false) })
	reg("gmatch", strGmatch)
	reg("gsub", strGsub)
	L.Globals.RawSet("string", s)
	L.stringLib = s
}

// ---- pattern matching (port of Lua 5.1 lstrlib.c) ----

const (
	capUnfinished = -1
	capPosition   = -2
	maxCaptures   = 32
)

type capture struct{ init, len int }

type matchState struct {
	L     *State
	src   string
	pat   string
	level int
	cap   [maxCaptures]capture
	depth int
}

func (ms *matchState) pc(p int) byte {
	if p < len(ms.pat) {
		return ms.pat[p]
	}
	return 0
}

func (ms *matchState) classEnd(p int) int {
	c := ms.pc(p)
	p++
	switch c {
	case '%':
		if p >= len(ms.pat) {
			ms.L.RuntimeError("malformed pattern (ends with '%%')")
		}
		return p + 1
	case '[':
		if ms.pc(p) == '^' {
			p++
		}
		for {
			if p >= len(ms.pat) {
				ms.L.RuntimeError("malformed pattern (missing ']')")
			}
			c := ms.pat[p]
			p++
			if c == '%' && p < len(ms.pat) {
				p++
			}
			if ms.pc(p) == ']' {
				break
			}
		}
		return p + 1
	}
	return p
}

func isalpha(c byte) bool  { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isdigitb(c byte) bool { return c >= '0' && c <= '9' }
func islower(c byte) bool  { return c >= 'a' && c <= 'z' }
func isupper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func isspace(c byte) bool  { return c == ' ' || (c >= '\t' && c <= '\r') }
func iscntrl(c byte) bool  { return c < 32 || c == 127 }
func ispunct(c byte) bool  { return c > 32 && c < 127 && !isalpha(c) && !isdigitb(c) }
func isxdigit(c byte) bool { return isdigitb(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

func matchClass(c, cl byte) bool {
	var res bool
	lc := cl
	if isupper(cl) {
		lc = cl + 32
	}
	switch lc {
	case 'a':
		res = isalpha(c)
	case 'c':
		res = iscntrl(c)
	case 'd':
		res = isdigitb(c)
	case 'l':
		res = islower(c)
	case 'p':
		res = ispunct(c)
	case 's':
		res = isspace(c)
	case 'u':
		res = isupper(c)
	case 'w':
		res = isalpha(c) || isdigitb(c)
	case 'x':
		res = isxdigit(c)
	case 'z':
		res = c == 0
	default:
		return cl == c
	}
	if isupper(cl) {
		return !res
	}
	return res
}

func (ms *matchState) matchBracketClass(c byte, p, ec int) bool {
	sig := true
	if ms.pc(p+1) == '^' {
		sig = false
		p++
	}
	for p++; p < ec; p++ {
		if ms.pat[p] == '%' {
			p++
			if matchClass(c, ms.pc(p)) {
				return sig
			}
		} else if ms.pc(p+1) == '-' && p+2 < ec {
			p += 2
			if ms.pat[p-2] <= c && c <= ms.pat[p] {
				return sig
			}
		} else if ms.pat[p] == c {
			return sig
		}
	}
	return !sig
}

func (ms *matchState) singleMatch(c byte, p, ep int) bool {
	switch ms.pc(p) {
	case '.':
		return true
	case '%':
		return matchClass(c, ms.pc(p+1))
	case '[':
		return ms.matchBracketClass(c, p, ep-1)
	}
	return ms.pc(p) == c
}

func (ms *matchState) match(s, p int) int {
	ms.depth++
	if ms.depth > 200 {
		ms.L.RuntimeError("pattern too complex")
	}
	defer func() { ms.depth-- }()
	for {
		if p >= len(ms.pat) {
			return s
		}
		switch ms.pat[p] {
		case '(':
			if ms.pc(p+1) == ')' {
				return ms.startCapture(s, p+2, capPosition)
			}
			return ms.startCapture(s, p+1, capUnfinished)
		case ')':
			return ms.endCapture(s, p+1)
		case '%':
			switch ms.pc(p + 1) {
			case 'b':
				s = ms.matchBalance(s, p+2)
				if s < 0 {
					return -1
				}
				p += 4
				continue
			case 'f':
				p += 2
				if ms.pc(p) != '[' {
					ms.L.RuntimeError("missing '[' after '%%f' in pattern")
				}
				ep := ms.classEnd(p)
				var prev byte
				if s > 0 {
					prev = ms.src[s-1]
				}
				var cur byte
				if s < len(ms.src) {
					cur = ms.src[s]
				}
				if ms.matchBracketClass(prev, p, ep-1) || !ms.matchBracketClass(cur, p, ep-1) {
					return -1
				}
				p = ep
				continue
			default:
				if isdigitb(ms.pc(p + 1)) {
					s = ms.matchCapture(s, ms.pc(p+1))
					if s < 0 {
						return -1
					}
					p += 2
					continue
				}
			}
		case '$':
			if p+1 == len(ms.pat) {
				if s == len(ms.src) {
					return s
				}
				return -1
			}
		}
		ep := ms.classEnd(p)
		m := s < len(ms.src) && ms.singleMatch(ms.src[s], p, ep)
		switch ms.pc(ep) {
		case '?':
			if m {
				if res := ms.match(s+1, ep+1); res >= 0 {
					return res
				}
			}
			p = ep + 1
			continue
		case '*':
			return ms.maxExpand(s, p, ep)
		case '+':
			if !m {
				return -1
			}
			return ms.maxExpand(s+1, p, ep)
		case '-':
			return ms.minExpand(s, p, ep)
		default:
			if !m {
				return -1
			}
			s++
			p = ep
		}
	}
}

func (ms *matchState) maxExpand(s, p, ep int) int {
	i := 0
	for s+i < len(ms.src) && ms.singleMatch(ms.src[s+i], p, ep) {
		i++
	}
	for i >= 0 {
		if res := ms.match(s+i, ep+1); res >= 0 {
			return res
		}
		i--
	}
	return -1
}

func (ms *matchState) minExpand(s, p, ep int) int {
	for {
		if res := ms.match(s, ep+1); res >= 0 {
			return res
		}
		if s < len(ms.src) && ms.singleMatch(ms.src[s], p, ep) {
			s++
		} else {
			return -1
		}
	}
}

func (ms *matchState) startCapture(s, p, what int) int {
	if ms.level >= maxCaptures {
		ms.L.RuntimeError("too many captures")
	}
	ms.cap[ms.level] = capture{init: s, len: what}
	ms.level++
	res := ms.match(s, p)
	if res < 0 {
		ms.level--
	}
	return res
}

func (ms *matchState) endCapture(s, p int) int {
	l := -1
	for i := ms.level - 1; i >= 0; i-- {
		if ms.cap[i].len == capUnfinished {
			l = i
			break
		}
	}
	if l < 0 {
		ms.L.RuntimeError("invalid pattern capture")
	}
	ms.cap[l].len = s - ms.cap[l].init
	res := ms.match(s, p)
	if res < 0 {
		ms.cap[l].len = capUnfinished
	}
	return res
}

func (ms *matchState) matchBalance(s, p int) int {
	if p+1 >= len(ms.pat) {
		ms.L.RuntimeError("unbalanced pattern")
	}
	if s >= len(ms.src) || ms.src[s] != ms.pat[p] {
		return -1
	}
	b, e := ms.pat[p], ms.pat[p+1]
	cont := 1
	for s++; s < len(ms.src); s++ {
		if ms.src[s] == e {
			cont--
			if cont == 0 {
				return s + 1
			}
		} else if ms.src[s] == b {
			cont++
		}
	}
	return -1
}

func (ms *matchState) matchCapture(s int, l byte) int {
	idx := int(l - '1')
	if idx < 0 || idx >= ms.level || ms.cap[idx].len == capUnfinished {
		ms.L.RuntimeError("invalid capture index")
	}
	c := ms.cap[idx]
	if len(ms.src)-s >= c.len && ms.src[c.init:c.init+c.len] == ms.src[s:s+c.len] {
		return s + c.len
	}
	return -1
}

func (ms *matchState) oneCapture(i, s, e int) Value {
	if i >= ms.level {
		if i == 0 {
			return ms.src[s:e]
		}
		ms.L.RuntimeError("invalid capture index")
	}
	c := ms.cap[i]
	if c.len == capUnfinished {
		ms.L.RuntimeError("unfinished capture")
	}
	if c.len == capPosition {
		return float64(c.init + 1)
	}
	return ms.src[c.init : c.init+c.len]
}

func (ms *matchState) captures(s, e int, wholeIfNone bool) []Value {
	n := ms.level
	if n == 0 && wholeIfNone {
		n = 1
	}
	out := make([]Value, n)
	for i := 0; i < n; i++ {
		out[i] = ms.oneCapture(i, s, e)
	}
	return out
}

const specials = "^$*+?.([%-"

func strFindAux(L *State, a []Value, find bool) []Value {
	name := "match"
	if find {
		name = "find"
	}
	s := L.checkString(a, 0, name)
	p := L.checkString(a, 1, name)
	init := posrelat(L.optInt(a, 2, name, 1), len(s)) - 1
	if init < 0 {
		init = 0
	} else if init > len(s) {
		init = len(s)
	}
	if find && (Truthy(arg(a, 3)) || !strings.ContainsAny(p, specials)) {
		idx := strings.Index(s[init:], p)
		if idx < 0 {
			return one(nil)
		}
		return []Value{float64(init + idx + 1), float64(init + idx + len(p))}
	}
	ms := &matchState{L: L, src: s, pat: p}
	anchor := len(p) > 0 && p[0] == '^'
	pstart := 0
	if anchor {
		pstart = 1
	}
	s1 := init
	for {
		ms.level = 0
		if e := ms.match(s1, pstart); e >= 0 {
			if find {
				return append([]Value{float64(s1 + 1), float64(e)}, ms.captures(-1, -1, false)...)
			}
			return ms.captures(s1, e, true)
		}
		s1++
		if s1 > len(s) || anchor {
			break
		}
	}
	return one(nil)
}

func strGmatch(L *State, a []Value) []Value {
	s := L.checkString(a, 0, "gmatch")
	p := L.checkString(a, 1, "gmatch")
	pos := 0
	return one(&GoFunction{Name: "gmatch_iter", Fn: func(L *State, _ []Value) []Value {
		ms := &matchState{L: L, src: s, pat: p}
		for src := pos; src <= len(s); src++ {
			ms.level = 0
			if e := ms.match(src, 0); e >= 0 {
				newstart := e
				if e == src {
					newstart++
				}
				pos = newstart
				return ms.captures(src, e, true)
			}
		}
		pos = len(s) + 1
		return one(nil)
	}})
}

func strGsub(L *State, a []Value) []Value {
	src := L.checkString(a, 0, "gsub")
	p := L.checkString(a, 1, "gsub")
	repl := arg(a, 2)
	switch repl.(type) {
	case string, float64, *Table, *Closure, *GoFunction:
	default:
		L.ArgError(3, "gsub", "string/function/table expected")
	}
	maxN := len(src) + 1
	if arg(a, 3) != nil {
		maxN = L.checkInt(a, 3, "gsub")
	}
	anchor := len(p) > 0 && p[0] == '^'
	pstart := 0
	if anchor {
		pstart = 1
	}
	ms := &matchState{L: L, src: src, pat: p}
	var b strings.Builder
	s := 0
	n := 0
	for n < maxN {
		ms.level = 0
		e := ms.match(s, pstart)
		if e >= 0 {
			n++
			gsubAddValue(ms, &b, s, e, repl)
		}
		if e >= 0 && e > s {
			s = e
		} else if s < len(src) {
			b.WriteByte(src[s])
			s++
		} else {
			break
		}
		if anchor {
			break
		}
	}
	b.WriteString(src[s:])
	return []Value{b.String(), float64(n)}
}

func gsubAddValue(ms *matchState, b *strings.Builder, s, e int, repl Value) {
	L := ms.L
	var v Value
	switch r := repl.(type) {
	case string, float64:
		rs, _ := ToStringCoerce(r)
		for i := 0; i < len(rs); i++ {
			if rs[i] != '%' {
				b.WriteByte(rs[i])
				continue
			}
			i++
			if i >= len(rs) {
				break
			}
			if !isdigitb(rs[i]) {
				b.WriteByte(rs[i])
			} else if rs[i] == '0' {
				b.WriteString(ms.src[s:e])
			} else {
				cv := ms.oneCapture(int(rs[i]-'1'), s, e)
				str, _ := ToStringCoerce(cv)
				b.WriteString(str)
			}
		}
		return
	case *Table:
		v = L.index(r, ms.oneCapture(0, s, e), "table")
	default:
		rets := L.Call(r, ms.captures(s, e, true))
		if len(rets) > 0 {
			v = rets[0]
		}
	}
	if !Truthy(v) {
		b.WriteString(ms.src[s:e])
		return
	}
	str, ok := ToStringCoerce(v)
	if !ok {
		L.RuntimeError("invalid replacement value (a %s)", TypeName(v))
	}
	b.WriteString(str)
}

// ---- string.format ----

func strFormat(L *State, a []Value) []Value {
	fs := L.checkString(a, 0, "format")
	argi := 1
	var b strings.Builder
	for i := 0; i < len(fs); i++ {
		c := fs[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(fs) {
			L.RuntimeError("invalid option '%%' to 'format'")
		}
		if fs[i] == '%' {
			b.WriteByte('%')
			continue
		}
		start := i
		for i < len(fs) && strings.IndexByte("-+ #0", fs[i]) >= 0 {
			i++
		}
		for i < len(fs) && isdigitb(fs[i]) {
			i++
		}
		if i < len(fs) && fs[i] == '.' {
			i++
			for i < len(fs) && isdigitb(fs[i]) {
				i++
			}
		}
		if i >= len(fs) {
			L.RuntimeError("invalid option '%%' to 'format'")
		}
		spec := fs[start:i]
		conv := fs[i]
		if argi >= len(a) && conv != '%' {
			L.ArgError(argi+1, "format", "no value")
		}
		switch conv {
		case 'c':
			b.WriteByte(byte(L.checkInt(a, argi, "format")))
		case 'd', 'i':
			n := L.checkNumber(a, argi, "format")
			b.WriteString(fmt.Sprintf("%"+spec+"d", int64(n)))
		case 'u':
			n := L.checkNumber(a, argi, "format")
			b.WriteString(fmt.Sprintf("%"+spec+"d", uint64(int64(n))))
		case 'o', 'x', 'X':
			n := L.checkNumber(a, argi, "format")
			b.WriteString(fmt.Sprintf("%"+spec+string(conv), uint64(int64(n))))
		case 'e', 'E', 'f', 'g', 'G':
			n := L.checkNumber(a, argi, "format")
			sp := spec
			if !strings.Contains(sp, ".") {
				sp += ".6"
			}
			out := fmt.Sprintf("%"+sp+string(conv), n)
			if conv == 'g' || conv == 'G' {
				out = trimGoG(out, spec)
			}
			b.WriteString(out)
		case 'q':
			s := L.checkString(a, argi, "format")
			b.WriteByte('"')
			for j := 0; j < len(s); j++ {
				switch s[j] {
				case '"', '\\':
					b.WriteByte('\\')
					b.WriteByte(s[j])
				case '\n':
					b.WriteString("\\\n")
				case '\r':
					b.WriteString("\\r")
				case 0:
					b.WriteString("\\000")
				default:
					b.WriteByte(s[j])
				}
			}
			b.WriteByte('"')
		case 's':
			s := L.ToStringMeta(arg(a, argi))
			b.WriteString(fmt.Sprintf("%"+spec+"s", s))
		default:
			L.RuntimeError("invalid option '%%%c' to 'format'", conv)
		}
		argi++
	}
	return one(b.String())
}

// trimGoG adapts Go's %g output to C's (Go keeps trailing zeros with '#').
func trimGoG(s, spec string) string {
	if strings.Contains(spec, "#") {
		return s
	}
	// Go's %.6g already drops trailing zeros like C; normalise exponent.
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp := s[i+2:]
		if len(exp) == 1 {
			s = s[:i+2] + "0" + exp
		}
	}
	return s
}

var _ = strconv.Itoa
