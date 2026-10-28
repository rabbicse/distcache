package lua

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Value is a Lua value: nil, bool, float64, string, *Table, *Closure or
// *GoFunction.
type Value = any

// GoFunction is a builtin implemented in Go.
type GoFunction struct {
	Name string
	Fn   func(L *State, args []Value) []Value
}

// Table is a Lua table with an array part and an insertion-ordered hash part
// (so next() is deterministic).
type Table struct {
	arr  []Value
	keys []Value
	idx  map[Value]int
	vals []Value
	dead int
	Meta *Table
	// ReadOnly makes writes raise an error (used for redis protected tables).
	ReadOnly bool
}

// NewTable creates a table.
func NewTable(narr, nhash int) *Table {
	t := &Table{}
	if narr > 0 {
		t.arr = make([]Value, 0, narr)
	}
	if nhash > 0 {
		t.idx = make(map[Value]int, nhash)
	}
	return t
}

func normKey(k Value) Value {
	if f, ok := k.(float64); ok && f == 0 {
		return float64(0)
	}
	return k
}

func arrayIndex(k Value) (int, bool) {
	f, ok := k.(float64)
	if !ok {
		return 0, false
	}
	i := int(f)
	if float64(i) != f || i < 1 {
		return 0, false
	}
	return i, true
}

// RawGet reads t[k] without metamethods.
func (t *Table) RawGet(k Value) Value {
	if i, ok := arrayIndex(k); ok && i <= len(t.arr) {
		return t.arr[i-1]
	}
	if t.idx == nil {
		return nil
	}
	if p, ok := t.idx[normKey(k)]; ok {
		return t.vals[p]
	}
	return nil
}

// GetStr reads a string key.
func (t *Table) GetStr(k string) Value { return t.RawGet(k) }

// RawSet writes t[k]=v without metamethods. k must not be nil/NaN.
func (t *Table) RawSet(k, v Value) {
	if i, ok := arrayIndex(k); ok {
		if i <= len(t.arr) {
			t.arr[i-1] = v
			if v == nil && i == len(t.arr) {
				// shrink trailing nils
				n := len(t.arr)
				for n > 0 && t.arr[n-1] == nil {
					n--
				}
				t.arr = t.arr[:n]
			}
			return
		}
		if i == len(t.arr)+1 {
			if v == nil {
				t.hashSet(k, nil)
				return
			}
			t.arr = append(t.arr, v)
			// migrate following keys from the hash part
			if t.idx != nil {
				t.hashSet(k, nil)
				for {
					nk := float64(len(t.arr) + 1)
					p, ok := t.idx[nk]
					if !ok || t.vals[p] == nil {
						break
					}
					t.arr = append(t.arr, t.vals[p])
					t.hashSet(nk, nil)
				}
			}
			return
		}
	}
	t.hashSet(normKey(k), v)
}

func (t *Table) hashSet(k, v Value) {
	if t.idx == nil {
		if v == nil {
			return
		}
		t.idx = make(map[Value]int)
	}
	if p, ok := t.idx[k]; ok {
		if t.vals[p] != nil && v == nil {
			t.dead++
		} else if t.vals[p] == nil && v != nil {
			t.dead--
		}
		t.vals[p] = v
		return
	}
	if v == nil {
		return
	}
	if t.dead > 32 && t.dead > len(t.keys)/2 {
		t.compact()
	}
	t.idx[k] = len(t.keys)
	t.keys = append(t.keys, k)
	t.vals = append(t.vals, v)
}

func (t *Table) compact() {
	nk := t.keys[:0]
	nv := t.vals[:0]
	t.idx = make(map[Value]int, len(t.keys)-t.dead)
	for i, k := range t.keys {
		if t.vals[i] != nil {
			t.idx[k] = len(nk)
			nk = append(nk, k)
			nv = append(nv, t.vals[i])
		}
	}
	t.keys, t.vals, t.dead = nk, nv, 0
}

// Len returns the border (#t).
func (t *Table) Len() int {
	n := len(t.arr)
	if n > 0 {
		return n
	}
	// Array part empty: probe hash for 1..
	i := 0
	for t.RawGet(float64(i+1)) != nil {
		i++
	}
	return i
}

// Append adds v at #t+1.
func (t *Table) Append(v Value) { t.RawSet(float64(t.Len()+1), v) }

// Next returns the key/value after k (nil starts). ok=false on bad key.
func (t *Table) Next(k Value) (Value, Value, bool) {
	start := 0
	if k != nil {
		if i, isArr := arrayIndex(k); isArr && i <= len(t.arr) {
			start = i
		} else {
			if t.idx == nil {
				return nil, nil, false
			}
			p, ok := t.idx[normKey(k)]
			if !ok {
				return nil, nil, false
			}
			start = len(t.arr) + p + 1
		}
	}
	for i := start; i < len(t.arr); i++ {
		if t.arr[i] != nil {
			return float64(i + 1), t.arr[i], true
		}
	}
	hstart := start - len(t.arr)
	if hstart < 0 {
		hstart = 0
	}
	for p := hstart; p < len(t.keys); p++ {
		if t.vals[p] != nil {
			return t.keys[p], t.vals[p], true
		}
	}
	return nil, nil, true
}

// ---- conversions ----

// TypeName returns the Lua type name.
func TypeName(v Value) string {
	switch v.(type) {
	case nil:
		return "nil"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case *Table:
		return "table"
	case *Closure, *GoFunction:
		return "function"
	}
	return "userdata"
}

// Truthy implements Lua truthiness.
func Truthy(v Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	}
	return true
}

// FormatNumber formats like "%.14g".
func FormatNumber(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		if math.Signbit(f) {
			return "-nan"
		}
		return "nan"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		if f == 0 && math.Signbit(f) {
			return "-0"
		}
		return strconv.FormatInt(int64(f), 10)
	}
	s := strconv.FormatFloat(f, 'g', 14, 64)
	// Go prints e+06 style already; trim trailing zeros in mantissa.
	if strings.ContainsAny(s, "e") {
		mant, exp, _ := strings.Cut(s, "e")
		if strings.Contains(mant, ".") {
			mant = strings.TrimRight(strings.TrimRight(mant, "0"), ".")
		}
		return mant + "e" + exp
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// ToNumber converts numbers and numeric strings.
func ToNumber(v Value) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		return str2number(x)
	}
	return 0, false
}

// ToStringCoerce converts strings and numbers to string.
func ToStringCoerce(v Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return FormatNumber(x), true
	}
	return "", false
}

// ToString implements tostring() without metamethods.
func ToString(v Value) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return FormatNumber(x)
	case string:
		return x
	case *Table:
		return fmt.Sprintf("table: %p", x)
	case *Closure:
		return fmt.Sprintf("function: %p", x)
	case *GoFunction:
		return fmt.Sprintf("function: builtin: %p", x)
	}
	return fmt.Sprintf("userdata: %v", v)
}

// rawEqual compares values.
func rawEqual(a, b Value) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return a == b
}
