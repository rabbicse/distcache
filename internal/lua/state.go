package lua

import (
	"fmt"
	"math"
	"runtime"
)

// LuaError is a Lua runtime error (catchable by pcall).
type LuaError struct{ Value Value }

func (e *LuaError) Error() string {
	if s, ok := e.Value.(string); ok {
		return s
	}
	if t, ok := e.Value.(*Table); ok {
		if s, ok := t.RawGet("err").(string); ok {
			return s
		}
	}
	return ToString(e.Value)
}

// FatalError aborts execution and cannot be caught by pcall (script kill,
// timeouts, out of memory).
type FatalError struct{ Msg string }

func (e *FatalError) Error() string { return e.Msg }

type cell struct{ v Value }

type upDesc struct {
	fromParent bool // parent local slot
	idx        int
}

type proto struct {
	name    string
	nparams int
	vararg  bool
	nslots  int
	body    []stmtFn
	ups     []upDesc
	line    int
}

// Closure is a Lua function value.
type Closure struct {
	p  *proto
	up []*cell
}

type frame struct {
	L       *State
	cl      *Closure
	slots   []*cell
	varargs []Value
	ret     []Value
}

// State is an interpreter instance.
type State struct {
	Globals *Table
	// Chunk is the chunk name used in error messages (e.g. "user_script").
	Chunk string
	// Hook is called every HookEvery statements; returning a non-nil
	// error aborts the script with a FatalError.
	Hook      func() error
	HookEvery int64

	// ProtectGlobals makes reads of undefined globals and writes of new
	// globals raise errors, like Redis.
	ProtectGlobals bool

	line      int
	depth     int
	steps     int64
	stringLib *Table
}

// NewState creates an interpreter with the standard Redis Lua libraries.
func NewState() *State {
	L := &State{Globals: NewTable(0, 64), Chunk: "user_script", HookEvery: 10000}
	openBase(L)
	openString(L)
	openTable(L)
	openMath(L)
	openBit(L)
	openCJSON(L)
	openCMsgpack(L)
	return L
}

// Line returns the current line.
func (L *State) Line() int { return L.line }

// Register sets a global function.
func (L *State) Register(name string, fn func(L *State, args []Value) []Value) {
	L.Globals.RawSet(name, &GoFunction{Name: name, Fn: fn})
}

// RuntimeError raises an error with position information.
func (L *State) RuntimeError(format string, args ...any) {
	panic(&LuaError{Value: fmt.Sprintf("%s:%d: %s", L.Chunk, L.line, fmt.Sprintf(format, args...))})
}

// Error raises an arbitrary value as error.
func (L *State) Error(v Value) { panic(&LuaError{Value: v}) }

// ArgError raises a bad argument error.
func (L *State) ArgError(n int, fname, msg string) {
	L.RuntimeError("bad argument #%d to '%s' (%s)", n, fname, msg)
}

func (L *State) tick() {
	L.steps++
	if L.Hook != nil && L.steps%L.HookEvery == 0 {
		if err := L.Hook(); err != nil {
			panic(&FatalError{Msg: err.Error()})
		}
	}
}

// Compile parses and compiles a chunk into a function.
func (L *State) Compile(src, chunk string) (cl *Closure, err error) {
	fb, err := parseChunk(src, chunk)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			if se, ok := r.(*SyntaxError); ok {
				err = se
				return
			}
			panic(r)
		}
	}()
	p := compileFunction(fb, nil, chunk)
	return &Closure{p: p}, nil
}

// PCall calls fn with args, converting Lua errors into Go errors.
func (L *State) PCall(fn Value, args ...Value) (rets []Value, err error) {
	depth := L.depth
	defer func() {
		if r := recover(); r != nil {
			L.depth = depth
			switch e := r.(type) {
			case *LuaError:
				err = e
			case *FatalError:
				err = e
			case runtime.Error:
				err = &LuaError{Value: fmt.Sprintf("%s:%d: internal error: %v", L.Chunk, L.line, e)}
			default:
				panic(r)
			}
		}
	}()
	return L.Call(fn, args), nil
}

// Call invokes a function value.
func (L *State) Call(fn Value, args []Value) []Value { return L.call(fn, args, "?") }

func (L *State) call(fn Value, args []Value, desc string) []Value {
	switch f := fn.(type) {
	case *Closure:
		return L.callClosure(f, args)
	case *GoFunction:
		L.depth++
		if L.depth > 200 {
			L.depth--
			L.RuntimeError("stack overflow")
		}
		r := f.Fn(L, args)
		L.depth--
		return r
	case *Table:
		if f.Meta != nil {
			if h := f.Meta.RawGet("__call"); h != nil {
				return L.call(h, append([]Value{f}, args...), desc)
			}
		}
	}
	L.RuntimeError("attempt to call %s", what(desc, fn))
	return nil
}

func (L *State) callClosure(cl *Closure, args []Value) []Value {
	L.depth++
	if L.depth > 200 {
		L.depth--
		L.RuntimeError("stack overflow")
	}
	p := cl.p
	f := &frame{L: L, cl: cl, slots: make([]*cell, p.nslots)}
	for i := 0; i < p.nparams; i++ {
		var v Value
		if i < len(args) {
			v = args[i]
		}
		f.slots[i] = &cell{v}
	}
	if p.vararg && len(args) > p.nparams {
		f.varargs = args[p.nparams:]
	}
	line := L.line
	for _, s := range p.body {
		if s(f) == ctrlReturn {
			break
		}
	}
	L.line = line
	L.depth--
	return f.ret
}

// ---- metamethod-aware operations ----

func (L *State) index(o, k Value, desc string) Value {
	switch t := o.(type) {
	case *Table:
		v := t.RawGet(k)
		if v != nil || t.Meta == nil {
			return v
		}
		h := t.Meta.RawGet("__index")
		switch hh := h.(type) {
		case nil:
			return nil
		case *Table:
			return L.index(hh, k, desc)
		default:
			r := L.call(hh, []Value{t, k}, "__index")
			if len(r) > 0 {
				return r[0]
			}
			return nil
		}
	case string:
		if L.stringLib != nil {
			return L.stringLib.RawGet(k)
		}
	}
	L.RuntimeError("attempt to index %s", what(desc, o))
	return nil
}

func (L *State) setIndex(o, k, v Value, desc string) {
	t, ok := o.(*Table)
	if !ok {
		L.RuntimeError("attempt to index %s", what(desc, o))
	}
	if k == nil {
		L.RuntimeError("table index is nil")
	}
	if f, isNum := k.(float64); isNum && math.IsNaN(f) {
		L.RuntimeError("table index is NaN")
	}
	if t.ReadOnly {
		L.RuntimeError("Attempt to modify a readonly table")
	}
	if t.Meta != nil && t.RawGet(k) == nil {
		if h := t.Meta.RawGet("__newindex"); h != nil {
			if ht, ok := h.(*Table); ok {
				L.setIndex(ht, k, v, desc)
				return
			}
			L.call(h, []Value{t, k, v}, "__newindex")
			return
		}
	}
	t.RawSet(k, v)
}

func (L *State) getGlobal(name string) Value {
	v := L.Globals.RawGet(name)
	if v == nil && L.ProtectGlobals {
		L.RuntimeError("Script attempted to access nonexistent global variable '%s'", name)
	}
	return v
}

func (L *State) setGlobal(name string, v Value) {
	if L.ProtectGlobals && L.Globals.RawGet(name) == nil {
		L.RuntimeError("Script attempted to create global variable '%s'", name)
	}
	if L.Globals.ReadOnly {
		L.RuntimeError("Attempt to modify a readonly table")
	}
	L.Globals.RawSet(name, v)
}

func (L *State) arith(op byte, a, b Value, da, db string) Value {
	x, ok1 := ToNumber(a)
	y, ok2 := ToNumber(b)
	if !ok1 || !ok2 {
		if mm := L.binMeta(a, b, arithEvent(op)); mm != nil {
			r := L.call(mm, []Value{a, b}, "metamethod")
			if len(r) > 0 {
				return r[0]
			}
			return nil
		}
		d, v := da, a
		if ok1 {
			d, v = db, b
		}
		L.RuntimeError("attempt to perform arithmetic on %s", what(d, v))
	}
	switch op {
	case '+':
		return x + y
	case '-':
		return x - y
	case '*':
		return x * y
	case '/':
		return x / y
	case '%':
		return x - math.Floor(x/y)*y
	case '^':
		return math.Pow(x, y)
	}
	return nil
}

func arithEvent(op byte) string {
	switch op {
	case '+':
		return "__add"
	case '-':
		return "__sub"
	case '*':
		return "__mul"
	case '/':
		return "__div"
	case '%':
		return "__mod"
	}
	return "__pow"
}

func (L *State) binMeta(a, b Value, event string) Value {
	if t, ok := a.(*Table); ok && t.Meta != nil {
		if h := t.Meta.RawGet(event); h != nil {
			return h
		}
	}
	if t, ok := b.(*Table); ok && t.Meta != nil {
		if h := t.Meta.RawGet(event); h != nil {
			return h
		}
	}
	return nil
}

func (L *State) concat(a, b Value, da, db string) Value {
	x, ok1 := ToStringCoerce(a)
	y, ok2 := ToStringCoerce(b)
	if !ok1 || !ok2 {
		if mm := L.binMeta(a, b, "__concat"); mm != nil {
			r := L.call(mm, []Value{a, b}, "metamethod")
			if len(r) > 0 {
				return r[0]
			}
			return nil
		}
		d, v := da, a
		if ok1 {
			d, v = db, b
		}
		L.RuntimeError("attempt to concatenate %s", what(d, v))
	}
	return x + y
}

func (L *State) lessThan(a, b Value) bool {
	switch x := a.(type) {
	case float64:
		if y, ok := b.(float64); ok {
			return x < y
		}
	case string:
		if y, ok := b.(string); ok {
			return x < y
		}
	}
	if mm := L.binMeta(a, b, "__lt"); mm != nil {
		r := L.call(mm, []Value{a, b}, "metamethod")
		return len(r) > 0 && Truthy(r[0])
	}
	L.compareError(a, b)
	return false
}

func (L *State) lessEqual(a, b Value) bool {
	switch x := a.(type) {
	case float64:
		if y, ok := b.(float64); ok {
			return x <= y
		}
	case string:
		if y, ok := b.(string); ok {
			return x <= y
		}
	}
	if mm := L.binMeta(a, b, "__le"); mm != nil {
		r := L.call(mm, []Value{a, b}, "metamethod")
		return len(r) > 0 && Truthy(r[0])
	}
	L.compareError(a, b)
	return false
}

func (L *State) compareError(a, b Value) {
	ta, tb := TypeName(a), TypeName(b)
	if ta == tb {
		L.RuntimeError("attempt to compare two %s values", ta)
	}
	L.RuntimeError("attempt to compare %s with %s", ta, tb)
}

func (L *State) equal(a, b Value) bool {
	if rawEqual(a, b) {
		return true
	}
	ta, ok1 := a.(*Table)
	tb, ok2 := b.(*Table)
	if ok1 && ok2 && ta.Meta != nil && tb.Meta != nil {
		h1 := ta.Meta.RawGet("__eq")
		h2 := tb.Meta.RawGet("__eq")
		if h1 != nil && rawEqual(h1, h2) {
			r := L.call(h1, []Value{a, b}, "metamethod")
			return len(r) > 0 && Truthy(r[0])
		}
	}
	return false
}

func (L *State) length(v Value, desc string) Value {
	switch x := v.(type) {
	case string:
		return float64(len(x))
	case *Table:
		return float64(x.Len())
	}
	L.RuntimeError("attempt to get length of %s", what(desc, v))
	return nil
}

// ToStringMeta implements tostring() honouring __tostring.
func (L *State) ToStringMeta(v Value) string {
	if t, ok := v.(*Table); ok && t.Meta != nil {
		if h := t.Meta.RawGet("__tostring"); h != nil {
			r := L.call(h, []Value{v}, "__tostring")
			if len(r) > 0 {
				if s, ok := r[0].(string); ok {
					return s
				}
			}
			L.RuntimeError("'__tostring' must return a string")
		}
	}
	return ToString(v)
}

// what renders an operand for error messages: "local 'x' (a nil value)" or
// "a nil value" when the operand has no name.
func what(desc string, v Value) string {
	if desc == "?" || desc == "" || desc == "a string value" {
		return "a " + TypeName(v) + " value"
	}
	return desc + " (a " + TypeName(v) + " value)"
}
