package lua

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

type lightUserdata struct{ name string }

// JSONNull is cjson.null.
var JSONNull = &lightUserdata{name: "cjson.null"}

func openCJSON(L *State) {
	t := NewTable(0, 4)
	t.RawSet("null", JSONNull)
	t.RawSet("encode", &GoFunction{Name: "encode", Fn: func(L *State, a []Value) []Value {
		if len(a) != 1 {
			L.RuntimeError("bad argument #1 to 'encode' (expected 1 argument)")
		}
		var b strings.Builder
		jsonEncode(L, &b, a[0], 0)
		return one(b.String())
	}})
	t.RawSet("decode", &GoFunction{Name: "decode", Fn: func(L *State, a []Value) []Value {
		s := L.checkString(a, 0, "decode")
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			L.RuntimeError("Expected value but found invalid token at character 1")
		}
		if dec.More() {
			L.RuntimeError("Expected the end but found invalid token")
		}
		return one(jsonToLua(v))
	}})
	t.RawSet("encode_keep_buffer", &GoFunction{Name: "encode_keep_buffer", Fn: func(L *State, a []Value) []Value { return nil }})
	t.RawSet("encode_max_depth", &GoFunction{Name: "encode_max_depth", Fn: func(L *State, a []Value) []Value { return one(float64(1000)) }})
	t.RawSet("decode_max_depth", &GoFunction{Name: "decode_max_depth", Fn: func(L *State, a []Value) []Value { return one(float64(1000)) }})
	t.RawSet("encode_invalid_numbers", &GoFunction{Name: "encode_invalid_numbers", Fn: func(L *State, a []Value) []Value { return one(false) }})
	t.RawSet("decode_invalid_numbers", &GoFunction{Name: "decode_invalid_numbers", Fn: func(L *State, a []Value) []Value { return one(false) }})
	L.Globals.RawSet("cjson", t)
}

func jsonToLua(v any) Value {
	switch x := v.(type) {
	case nil:
		return JSONNull
	case bool:
		return x
	case json.Number:
		f, _ := strconv.ParseFloat(string(x), 64)
		return f
	case string:
		return x
	case []any:
		t := NewTable(len(x), 0)
		for i, e := range x {
			t.RawSet(float64(i+1), jsonToLua(e))
		}
		return t
	case map[string]any:
		t := NewTable(0, len(x))
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.RawSet(k, jsonToLua(x[k]))
		}
		return t
	}
	return nil
}

func jsonString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '/':
			b.WriteString(`\/`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(c)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(c)&15, 16))
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// tableArrayLen returns n>0 when t is a pure array 1..n (cjson rules);
// -1 when it is an object; 0 for an empty table.
func tableArrayLen(L *State, t *Table) int {
	max := 0
	items := 0
	var k Value
	for {
		nk, _, _ := t.Next(k)
		if nk == nil {
			break
		}
		f, ok := nk.(float64)
		if !ok || f < 1 || f != math.Floor(f) {
			return -1
		}
		if int(f) > max {
			max = int(f)
		}
		items++
		k = nk
	}
	if max > 10 && max > items*2 {
		L.RuntimeError("Cannot serialise table: excessively sparse array")
	}
	return max
}

func jsonEncode(L *State, b *strings.Builder, v Value, depth int) {
	if depth > 1000 {
		L.RuntimeError("Cannot serialise, excessive nesting (1001)")
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case *lightUserdata:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			L.RuntimeError("Cannot serialise number: must not be NaN or Inf")
		}
		b.WriteString(FormatNumber(x))
	case string:
		jsonString(b, x)
	case *Table:
		n := tableArrayLen(L, x)
		if n > 0 {
			b.WriteByte('[')
			for i := 1; i <= n; i++ {
				if i > 1 {
					b.WriteByte(',')
				}
				jsonEncode(L, b, x.RawGet(float64(i)), depth+1)
			}
			b.WriteByte(']')
			return
		}
		b.WriteByte('{')
		first := true
		var k Value
		for {
			nk, nv, _ := x.Next(k)
			if nk == nil {
				break
			}
			k = nk
			if !first {
				b.WriteByte(',')
			}
			first = false
			switch kk := nk.(type) {
			case string:
				jsonString(b, kk)
			case float64:
				jsonString(b, FormatNumber(kk))
			default:
				L.RuntimeError("Cannot serialise %s: table key must be a number or string", TypeName(nk))
			}
			b.WriteByte(':')
			jsonEncode(L, b, nv, depth+1)
		}
		b.WriteByte('}')
	default:
		L.RuntimeError("Cannot serialise %s: type not supported", TypeName(v))
	}
}

// ---- cmsgpack ----

func openCMsgpack(L *State) {
	t := NewTable(0, 2)
	t.RawSet("pack", &GoFunction{Name: "pack", Fn: func(L *State, a []Value) []Value {
		if len(a) == 0 {
			L.RuntimeError("MessagePack pack needs input.")
		}
		var buf bytes.Buffer
		for _, v := range a {
			mpEncode(L, &buf, v, 0)
		}
		return one(buf.String())
	}})
	t.RawSet("unpack", &GoFunction{Name: "unpack", Fn: func(L *State, a []Value) []Value {
		s := []byte(L.checkString(a, 0, "unpack"))
		var out []Value
		for len(s) > 0 {
			v, rest, ok := mpDecode(s, 0)
			if !ok {
				L.RuntimeError("Missing bytes in input.")
			}
			out = append(out, v)
			s = rest
		}
		return out
	}})
	L.Globals.RawSet("cmsgpack", t)
}

func mpEncode(L *State, b *bytes.Buffer, v Value, depth int) {
	if depth > 16 {
		b.WriteByte(0xc0)
		return
	}
	switch x := v.(type) {
	case nil, *lightUserdata:
		b.WriteByte(0xc0)
	case bool:
		if x {
			b.WriteByte(0xc3)
		} else {
			b.WriteByte(0xc2)
		}
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x <= math.MaxInt64 && !math.IsInf(x, 0) {
			n := int64(x)
			switch {
			case n >= 0 && n <= 127:
				b.WriteByte(byte(n))
			case n < 0 && n >= -32:
				b.WriteByte(byte(n))
			case n >= 0 && n <= math.MaxUint8:
				b.WriteByte(0xcc)
				b.WriteByte(byte(n))
			case n >= 0 && n <= math.MaxUint16:
				b.WriteByte(0xcd)
				_ = binary.Write(b, binary.BigEndian, uint16(n))
			case n >= 0 && n <= math.MaxUint32:
				b.WriteByte(0xce)
				_ = binary.Write(b, binary.BigEndian, uint32(n))
			case n >= 0:
				b.WriteByte(0xcf)
				_ = binary.Write(b, binary.BigEndian, uint64(n))
			case n >= math.MinInt8:
				b.WriteByte(0xd0)
				b.WriteByte(byte(n))
			case n >= math.MinInt16:
				b.WriteByte(0xd1)
				_ = binary.Write(b, binary.BigEndian, int16(n))
			case n >= math.MinInt32:
				b.WriteByte(0xd2)
				_ = binary.Write(b, binary.BigEndian, int32(n))
			default:
				b.WriteByte(0xd3)
				_ = binary.Write(b, binary.BigEndian, n)
			}
			return
		}
		if float64(float32(x)) == x {
			b.WriteByte(0xca)
			_ = binary.Write(b, binary.BigEndian, float32(x))
		} else {
			b.WriteByte(0xcb)
			_ = binary.Write(b, binary.BigEndian, x)
		}
	case string:
		n := len(x)
		switch {
		case n < 32:
			b.WriteByte(0xa0 | byte(n))
		case n <= math.MaxUint8:
			b.WriteByte(0xd9)
			b.WriteByte(byte(n))
		case n <= math.MaxUint16:
			b.WriteByte(0xda)
			_ = binary.Write(b, binary.BigEndian, uint16(n))
		default:
			b.WriteByte(0xdb)
			_ = binary.Write(b, binary.BigEndian, uint32(n))
		}
		b.WriteString(x)
	case *Table:
		n := tableArrayLenNoErr(x)
		if n >= 0 {
			mpHeader(b, n, 0x90, 0xdc, 0xdd)
			for i := 1; i <= n; i++ {
				mpEncode(L, b, x.RawGet(float64(i)), depth+1)
			}
			return
		}
		cnt := 0
		var k Value
		for {
			nk, _, _ := x.Next(k)
			if nk == nil {
				break
			}
			cnt++
			k = nk
		}
		mpHeader(b, cnt, 0x80, 0xde, 0xdf)
		k = nil
		for {
			nk, nv, _ := x.Next(k)
			if nk == nil {
				break
			}
			mpEncode(L, b, nk, depth+1)
			mpEncode(L, b, nv, depth+1)
			k = nk
		}
	default:
		b.WriteByte(0xc0)
	}
}

func tableArrayLenNoErr(t *Table) int {
	max, items := 0, 0
	var k Value
	for {
		nk, _, _ := t.Next(k)
		if nk == nil {
			break
		}
		f, ok := nk.(float64)
		if !ok || f < 1 || f != math.Floor(f) {
			return -1
		}
		if int(f) > max {
			max = int(f)
		}
		items++
		k = nk
	}
	if max != items {
		return -1
	}
	return max
}

func mpHeader(b *bytes.Buffer, n int, fix, b16, b32 byte) {
	switch {
	case n < 16:
		b.WriteByte(fix | byte(n))
	case n <= math.MaxUint16:
		b.WriteByte(b16)
		_ = binary.Write(b, binary.BigEndian, uint16(n))
	default:
		b.WriteByte(b32)
		_ = binary.Write(b, binary.BigEndian, uint32(n))
	}
}

func mpDecode(s []byte, depth int) (Value, []byte, bool) {
	if len(s) == 0 || depth > 64 {
		return nil, nil, false
	}
	c := s[0]
	s = s[1:]
	need := func(n int) bool { return len(s) >= n }
	switch {
	case c <= 0x7f:
		return float64(c), s, true
	case c >= 0xe0:
		return float64(int8(c)), s, true
	case c&0xe0 == 0xa0:
		n := int(c & 0x1f)
		if !need(n) {
			return nil, nil, false
		}
		return string(s[:n]), s[n:], true
	case c&0xf0 == 0x90:
		return mpArray(s, int(c&0x0f), depth)
	case c&0xf0 == 0x80:
		return mpMap(s, int(c&0x0f), depth)
	}
	switch c {
	case 0xc0:
		return nil, s, true
	case 0xc2:
		return false, s, true
	case 0xc3:
		return true, s, true
	case 0xcc:
		if !need(1) {
			return nil, nil, false
		}
		return float64(s[0]), s[1:], true
	case 0xcd:
		if !need(2) {
			return nil, nil, false
		}
		return float64(binary.BigEndian.Uint16(s)), s[2:], true
	case 0xce:
		if !need(4) {
			return nil, nil, false
		}
		return float64(binary.BigEndian.Uint32(s)), s[4:], true
	case 0xcf:
		if !need(8) {
			return nil, nil, false
		}
		return float64(binary.BigEndian.Uint64(s)), s[8:], true
	case 0xd0:
		if !need(1) {
			return nil, nil, false
		}
		return float64(int8(s[0])), s[1:], true
	case 0xd1:
		if !need(2) {
			return nil, nil, false
		}
		return float64(int16(binary.BigEndian.Uint16(s))), s[2:], true
	case 0xd2:
		if !need(4) {
			return nil, nil, false
		}
		return float64(int32(binary.BigEndian.Uint32(s))), s[4:], true
	case 0xd3:
		if !need(8) {
			return nil, nil, false
		}
		return float64(int64(binary.BigEndian.Uint64(s))), s[8:], true
	case 0xca:
		if !need(4) {
			return nil, nil, false
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(s))), s[4:], true
	case 0xcb:
		if !need(8) {
			return nil, nil, false
		}
		return math.Float64frombits(binary.BigEndian.Uint64(s)), s[8:], true
	case 0xd9, 0xc4:
		if !need(1) {
			return nil, nil, false
		}
		n := int(s[0])
		s = s[1:]
		if !need(n) {
			return nil, nil, false
		}
		return string(s[:n]), s[n:], true
	case 0xda, 0xc5:
		if !need(2) {
			return nil, nil, false
		}
		n := int(binary.BigEndian.Uint16(s))
		s = s[2:]
		if !need(n) {
			return nil, nil, false
		}
		return string(s[:n]), s[n:], true
	case 0xdb, 0xc6:
		if !need(4) {
			return nil, nil, false
		}
		n := int(binary.BigEndian.Uint32(s))
		s = s[4:]
		if !need(n) {
			return nil, nil, false
		}
		return string(s[:n]), s[n:], true
	case 0xdc:
		if !need(2) {
			return nil, nil, false
		}
		return mpArray(s[2:], int(binary.BigEndian.Uint16(s)), depth)
	case 0xdd:
		if !need(4) {
			return nil, nil, false
		}
		return mpArray(s[4:], int(binary.BigEndian.Uint32(s)), depth)
	case 0xde:
		if !need(2) {
			return nil, nil, false
		}
		return mpMap(s[2:], int(binary.BigEndian.Uint16(s)), depth)
	case 0xdf:
		if !need(4) {
			return nil, nil, false
		}
		return mpMap(s[4:], int(binary.BigEndian.Uint32(s)), depth)
	}
	return nil, nil, false
}

func mpArray(s []byte, n, depth int) (Value, []byte, bool) {
	t := NewTable(n, 0)
	for i := 1; i <= n; i++ {
		v, rest, ok := mpDecode(s, depth+1)
		if !ok {
			return nil, nil, false
		}
		t.RawSet(float64(i), v)
		s = rest
	}
	return t, s, true
}

func mpMap(s []byte, n, depth int) (Value, []byte, bool) {
	t := NewTable(0, n)
	for i := 0; i < n; i++ {
		k, rest, ok := mpDecode(s, depth+1)
		if !ok {
			return nil, nil, false
		}
		v, rest2, ok := mpDecode(rest, depth+1)
		if !ok {
			return nil, nil, false
		}
		if k != nil {
			t.RawSet(k, v)
		}
		s = rest2
	}
	return t, s, true
}
