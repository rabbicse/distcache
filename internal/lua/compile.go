package lua

import "fmt"

const (
	ctrlNormal = iota
	ctrlBreak
	ctrlReturn
)

type stmtFn func(f *frame) int
type exprFn func(f *frame) Value
type multiFn func(f *frame) []Value

type localInfo struct {
	name string
	slot int
}

type fstate struct {
	parent   *fstate
	scopes   [][]*localInfo
	active   int
	maxSlots int
	ups      []upDesc
	upKeys   map[*localInfo]int
	vararg   bool
	loops    int
	chunk    string
}

func (fs *fstate) openScope() { fs.scopes = append(fs.scopes, nil) }
func (fs *fstate) closeScope() {
	top := fs.scopes[len(fs.scopes)-1]
	fs.active -= len(top)
	fs.scopes = fs.scopes[:len(fs.scopes)-1]
}

func (fs *fstate) declare(name string) *localInfo {
	li := &localInfo{name: name, slot: fs.active}
	fs.active++
	if fs.active > fs.maxSlots {
		fs.maxSlots = fs.active
	}
	fs.scopes[len(fs.scopes)-1] = append(fs.scopes[len(fs.scopes)-1], li)
	return li
}

const (
	varLocal = iota
	varUp
	varGlobal
)

func (fs *fstate) findLocal(name string) *localInfo {
	for i := len(fs.scopes) - 1; i >= 0; i-- {
		sc := fs.scopes[i]
		for j := len(sc) - 1; j >= 0; j-- {
			if sc[j].name == name {
				return sc[j]
			}
		}
	}
	return nil
}

// resolve returns the kind and index of a variable; li identifies the
// captured local for upvalue de-duplication.
func (fs *fstate) resolve(name string) (int, int, *localInfo) {
	if li := fs.findLocal(name); li != nil {
		return varLocal, li.slot, li
	}
	if fs.parent == nil {
		return varGlobal, 0, nil
	}
	kind, idx, li := fs.parent.resolve(name)
	if kind == varGlobal {
		return varGlobal, 0, nil
	}
	if fs.upKeys == nil {
		fs.upKeys = map[*localInfo]int{}
	}
	if j, ok := fs.upKeys[li]; ok {
		return varUp, j, li
	}
	fs.ups = append(fs.ups, upDesc{fromParent: kind == varLocal, idx: idx})
	j := len(fs.ups) - 1
	fs.upKeys[li] = j
	return varUp, j, li
}

func compileFunction(fb *funcBody, parent *fstate, chunk string) *proto {
	fs := &fstate{parent: parent, vararg: fb.vararg, chunk: chunk}
	fs.openScope()
	for _, p := range fb.params {
		fs.declare(p)
	}
	body := fs.block(fb.body, false)
	fs.closeScope()
	return &proto{name: fb.name, nparams: len(fb.params), vararg: fb.vararg, nslots: fs.maxSlots,
		body: body, ups: fs.ups, line: fb.line}
}

func (fs *fstate) block(stmts []stmt, newScope bool) []stmtFn {
	if newScope {
		fs.openScope()
		defer fs.closeScope()
	}
	out := make([]stmtFn, 0, len(stmts))
	for _, s := range stmts {
		out = append(out, fs.stmt(s))
	}
	return out
}

func runBlock(f *frame, body []stmtFn) int {
	for _, s := range body {
		if c := s(f); c != ctrlNormal {
			return c
		}
	}
	return ctrlNormal
}

func (fs *fstate) syntaxErr(line int, msg string) {
	panic(&SyntaxError{Msg: fmt.Sprintf("%s:%d: %s", fs.chunk, line, msg)})
}

func withLine(line int, fn stmtFn) stmtFn {
	return func(f *frame) int {
		f.L.line = line
		f.L.tick()
		return fn(f)
	}
}

func (fs *fstate) stmt(s stmt) stmtFn {
	switch st := s.(type) {
	case *sLocal:
		vals := fs.explist(st.exprs)
		// Declare after evaluating expressions (local x = x).
		lis := make([]*localInfo, len(st.names))
		for i, n := range st.names {
			lis[i] = fs.declare(n)
		}
		n := len(lis)
		if len(st.exprs) == 1 && n == 1 {
			e := fs.expr(st.exprs[0])
			slot := lis[0].slot
			return withLine(st.line, func(f *frame) int {
				f.slots[slot] = &cell{e(f)}
				return ctrlNormal
			})
		}
		return withLine(st.line, func(f *frame) int {
			vs := vals(f, n)
			for i, li := range lis {
				f.slots[li.slot] = &cell{vs[i]}
			}
			return ctrlNormal
		})
	case *sLocalFunction:
		li := fs.declare(st.name)
		mk := fs.closure(st.f)
		slot := li.slot
		return withLine(st.line, func(f *frame) int {
			c := &cell{}
			f.slots[slot] = c
			c.v = mk(f)
			return ctrlNormal
		})
	case *sAssign:
		vals := fs.explist(st.exprs)
		setters := make([]func(f *frame, v Value), len(st.targets))
		for i, t := range st.targets {
			setters[i] = fs.assigner(t)
		}
		n := len(setters)
		if n == 1 && len(st.exprs) == 1 {
			e := fs.expr(st.exprs[0])
			set := setters[0]
			return withLine(st.line, func(f *frame) int {
				set(f, e(f))
				return ctrlNormal
			})
		}
		return withLine(st.line, func(f *frame) int {
			vs := vals(f, n)
			for i, set := range setters {
				set(f, vs[i])
			}
			return ctrlNormal
		})
	case *sCall:
		m := fs.multi(st.call)
		return withLine(st.line, func(f *frame) int {
			m(f)
			return ctrlNormal
		})
	case *sDo:
		body := fs.block(st.body, true)
		return func(f *frame) int { return runBlock(f, body) }
	case *sWhile:
		cond := fs.expr(st.cond)
		fs.loops++
		body := fs.block(st.body, true)
		fs.loops--
		return withLine(st.line, func(f *frame) int {
			for Truthy(cond(f)) {
				f.L.tick()
				switch runBlock(f, body) {
				case ctrlBreak:
					return ctrlNormal
				case ctrlReturn:
					return ctrlReturn
				}
			}
			return ctrlNormal
		})
	case *sRepeat:
		fs.loops++
		fs.openScope()
		body := fs.block(st.body, false)
		cond := fs.expr(st.cond)
		fs.closeScope()
		fs.loops--
		return withLine(st.line, func(f *frame) int {
			for {
				f.L.tick()
				switch runBlock(f, body) {
				case ctrlBreak:
					return ctrlNormal
				case ctrlReturn:
					return ctrlReturn
				}
				if Truthy(cond(f)) {
					return ctrlNormal
				}
			}
		})
	case *sIf:
		conds := make([]exprFn, len(st.conds))
		blocks := make([][]stmtFn, len(st.blocks))
		for i := range st.conds {
			conds[i] = fs.expr(st.conds[i])
			blocks[i] = fs.block(st.blocks[i], true)
		}
		var els []stmtFn
		if st.els != nil {
			els = fs.block(st.els, true)
		}
		return withLine(st.line, func(f *frame) int {
			for i, c := range conds {
				if Truthy(c(f)) {
					return runBlock(f, blocks[i])
				}
			}
			if els != nil {
				return runBlock(f, els)
			}
			return ctrlNormal
		})
	case *sNumFor:
		start := fs.expr(st.start)
		limit := fs.expr(st.limit)
		var step exprFn
		if st.step != nil {
			step = fs.expr(st.step)
		}
		fs.openScope()
		fs.loops++
		li := fs.declare(st.v)
		body := fs.block(st.body, true)
		fs.loops--
		fs.closeScope()
		slot := li.slot
		return withLine(st.line, func(f *frame) int {
			a, ok1 := ToNumber(start(f))
			if !ok1 {
				f.L.RuntimeError("'for' initial value must be a number")
			}
			b, ok2 := ToNumber(limit(f))
			if !ok2 {
				f.L.RuntimeError("'for' limit must be a number")
			}
			s := 1.0
			if step != nil {
				var ok3 bool
				s, ok3 = ToNumber(step(f))
				if !ok3 {
					f.L.RuntimeError("'for' step must be a number")
				}
			}
			for i := a; (s > 0 && i <= b) || (s <= 0 && i >= b); i += s {
				f.slots[slot] = &cell{i}
				switch runBlock(f, body) {
				case ctrlBreak:
					return ctrlNormal
				case ctrlReturn:
					return ctrlReturn
				}
				f.L.tick()
			}
			return ctrlNormal
		})
	case *sGenFor:
		vals := fs.explist(st.exprs)
		fs.openScope()
		fs.loops++
		lis := make([]*localInfo, len(st.names))
		for i, n := range st.names {
			lis[i] = fs.declare(n)
		}
		body := fs.block(st.body, true)
		fs.loops--
		fs.closeScope()
		return withLine(st.line, func(f *frame) int {
			init := vals(f, 3)
			fn, state, ctl := init[0], init[1], init[2]
			for {
				f.L.tick()
				rs := f.L.call(fn, []Value{state, ctl}, "'for' iterator")
				var first Value
				if len(rs) > 0 {
					first = rs[0]
				}
				if first == nil {
					return ctrlNormal
				}
				ctl = first
				for i, li := range lis {
					var v Value
					if i < len(rs) {
						v = rs[i]
					}
					f.slots[li.slot] = &cell{v}
				}
				switch runBlock(f, body) {
				case ctrlBreak:
					return ctrlNormal
				case ctrlReturn:
					return ctrlReturn
				}
			}
		})
	case *sFunction:
		mk := fs.closure(st.f)
		if len(st.path) == 1 && st.method == "" {
			set := fs.assigner(&eName{name: st.path[0], line: st.line})
			return withLine(st.line, func(f *frame) int {
				set(f, mk(f))
				return ctrlNormal
			})
		}
		var obj expr = &eName{name: st.path[0], line: st.line}
		keys := st.path[1:]
		last := st.method
		if last == "" {
			last = keys[len(keys)-1]
			keys = keys[:len(keys)-1]
		}
		for _, k := range keys {
			obj = &eIndex{obj: obj, key: &eString{v: k}, line: st.line}
		}
		set := fs.assigner(&eIndex{obj: obj, key: &eString{v: last}, line: st.line})
		return withLine(st.line, func(f *frame) int {
			set(f, mk(f))
			return ctrlNormal
		})
	case *sReturn:
		if len(st.exprs) == 1 {
			if isMulti(st.exprs[0]) {
				m := fs.multi(st.exprs[0])
				return withLine(st.line, func(f *frame) int {
					f.ret = m(f)
					return ctrlReturn
				})
			}
			e := fs.expr(st.exprs[0])
			return withLine(st.line, func(f *frame) int {
				f.ret = []Value{e(f)}
				return ctrlReturn
			})
		}
		vals := fs.explist(st.exprs)
		return withLine(st.line, func(f *frame) int {
			f.ret = vals(f, -1)
			return ctrlReturn
		})
	case *sBreak:
		if fs.loops == 0 {
			fs.syntaxErr(st.line, "no loop to break near 'break'")
		}
		return func(f *frame) int { return ctrlBreak }
	}
	panic(fmt.Sprintf("lua: unknown statement %T", s))
}

func isMulti(e expr) bool {
	switch e.(type) {
	case *eCall, *eMethod, *eVararg:
		return true
	}
	return false
}

// explist compiles an expression list; the result function returns exactly
// want values (padding with nil) or all values when want < 0.
func (fs *fstate) explist(es []expr) func(f *frame, want int) []Value {
	if len(es) == 0 {
		return func(f *frame, want int) []Value {
			if want < 0 {
				return nil
			}
			return make([]Value, want)
		}
	}
	singles := make([]exprFn, len(es)-1)
	for i := range singles {
		singles[i] = fs.expr(es[i])
	}
	lastE := es[len(es)-1]
	var lastM multiFn
	var lastS exprFn
	if isMulti(lastE) {
		lastM = fs.multi(lastE)
	} else {
		lastS = fs.expr(lastE)
	}
	return func(f *frame, want int) []Value {
		out := make([]Value, 0, len(es))
		for _, s := range singles {
			out = append(out, s(f))
		}
		if lastM != nil {
			out = append(out, lastM(f)...)
		} else {
			out = append(out, lastS(f))
		}
		if want >= 0 {
			for len(out) < want {
				out = append(out, nil)
			}
			out = out[:want]
		}
		return out
	}
}

func (fs *fstate) describe(e expr) string {
	switch x := e.(type) {
	case *eName:
		kind, _, _ := fs.resolveNoCapture(x.name)
		switch kind {
		case varLocal:
			return "local '" + x.name + "'"
		case varUp:
			return "upvalue '" + x.name + "'"
		}
		return "global '" + x.name + "'"
	case *eIndex:
		if s, ok := x.key.(*eString); ok {
			return "field '" + s.v + "'"
		}
	case *eMethod:
		return "method '" + x.name + "'"
	case *eString:
		return "a string value"
	}
	return "?"
}

func (fs *fstate) resolveNoCapture(name string) (int, int, *localInfo) {
	if li := fs.findLocal(name); li != nil {
		return varLocal, li.slot, li
	}
	for p := fs.parent; p != nil; p = p.parent {
		if li := p.findLocal(name); li != nil {
			return varUp, 0, li
		}
	}
	return varGlobal, 0, nil
}

func (fs *fstate) assigner(t expr) func(f *frame, v Value) {
	switch x := t.(type) {
	case *eName:
		kind, idx, _ := fs.resolve(x.name)
		switch kind {
		case varLocal:
			return func(f *frame, v Value) { f.slots[idx].v = v }
		case varUp:
			return func(f *frame, v Value) { f.cl.up[idx].v = v }
		}
		name := x.name
		return func(f *frame, v Value) { f.L.setGlobal(name, v) }
	case *eIndex:
		obj := fs.expr(x.obj)
		key := fs.expr(x.key)
		desc := fs.describe(x.obj)
		return func(f *frame, v Value) { f.L.setIndex(obj(f), key(f), v, desc) }
	}
	panic("lua: bad assignment target")
}

func (fs *fstate) closure(fb *funcBody) exprFn {
	p := compileFunction(fb, fs, fs.chunk)
	ups := p.ups
	return func(f *frame) Value {
		cl := &Closure{p: p, up: make([]*cell, len(ups))}
		for i, u := range ups {
			if u.fromParent {
				c := f.slots[u.idx]
				if c == nil {
					c = &cell{}
					f.slots[u.idx] = c
				}
				cl.up[i] = c
			} else {
				cl.up[i] = f.cl.up[u.idx]
			}
		}
		return cl
	}
}

// multi compiles a multi-valued expression (call or vararg).
func (fs *fstate) multi(e expr) multiFn {
	switch x := e.(type) {
	case *eVararg:
		if !fs.vararg {
			fs.syntaxErr(0, "cannot use '...' outside a vararg function near '...'")
		}
		return func(f *frame) []Value {
			out := make([]Value, len(f.varargs))
			copy(out, f.varargs)
			return out
		}
	case *eCall:
		fn := fs.expr(x.fn)
		args := fs.explist(x.args)
		desc := fs.describe(x.fn)
		line := x.line
		return func(f *frame) []Value {
			fv := fn(f)
			a := args(f, -1)
			f.L.line = line
			return f.L.call(fv, a, desc)
		}
	case *eMethod:
		obj := fs.expr(x.obj)
		args := fs.explist(x.args)
		name := x.name
		odesc := fs.describe(x.obj)
		line := x.line
		return func(f *frame) []Value {
			o := obj(f)
			f.L.line = line
			m := f.L.index(o, name, odesc)
			a := args(f, -1)
			f.L.line = line
			return f.L.call(m, append([]Value{o}, a...), "method '"+name+"'")
		}
	}
	single := fs.expr(e)
	return func(f *frame) []Value { return []Value{single(f)} }
}

func (fs *fstate) expr(e expr) exprFn {
	switch x := e.(type) {
	case *eNil:
		return func(*frame) Value { return nil }
	case *eTrue:
		return func(*frame) Value { return true }
	case *eFalse:
		return func(*frame) Value { return false }
	case *eNumber:
		v := x.v
		return func(*frame) Value { return v }
	case *eString:
		v := x.v
		return func(*frame) Value { return v }
	case *eVararg:
		m := fs.multi(x)
		return func(f *frame) Value {
			vs := m(f)
			if len(vs) == 0 {
				return nil
			}
			return vs[0]
		}
	case *eFunction:
		return fs.closure(x.f)
	case *eParen:
		return fs.expr(x.e)
	case *eName:
		kind, idx, _ := fs.resolve(x.name)
		switch kind {
		case varLocal:
			return func(f *frame) Value { return f.slots[idx].v }
		case varUp:
			return func(f *frame) Value { return f.cl.up[idx].v }
		}
		name := x.name
		line := x.line
		return func(f *frame) Value {
			v := f.L.Globals.RawGet(name)
			if v == nil {
				f.L.line = line
				return f.L.getGlobal(name)
			}
			return v
		}
	case *eIndex:
		obj := fs.expr(x.obj)
		key := fs.expr(x.key)
		desc := fs.describe(x.obj)
		line := x.line
		return func(f *frame) Value {
			o := obj(f)
			if t, ok := o.(*Table); ok && t.Meta == nil {
				return t.RawGet(key(f))
			}
			k := key(f)
			f.L.line = line
			return f.L.index(o, k, desc)
		}
	case *eCall, *eMethod:
		m := fs.multi(x)
		return func(f *frame) Value {
			vs := m(f)
			if len(vs) == 0 {
				return nil
			}
			return vs[0]
		}
	case *eTable:
		items := make([]exprFn, len(x.items))
		lastMulti := multiFn(nil)
		for i, it := range x.items {
			if i == len(x.items)-1 && isMulti(it) {
				lastMulti = fs.multi(it)
			} else {
				items[i] = fs.expr(it)
			}
		}
		keys := make([]exprFn, len(x.keys))
		vals := make([]exprFn, len(x.vals))
		for i := range x.keys {
			keys[i] = fs.expr(x.keys[i])
			vals[i] = fs.expr(x.vals[i])
		}
		order := x.order
		return func(f *frame) Value {
			t := NewTable(len(items), len(keys))
			pos := 0
			for _, o := range order {
				if o >= 0 {
					k := keys[o](f)
					if k == nil {
						f.L.RuntimeError("table index is nil")
					}
					t.RawSet(k, vals[o](f))
					continue
				}
				i := -o - 1
				if items[i] == nil {
					for _, v := range lastMulti(f) {
						pos++
						t.RawSet(float64(pos), v)
					}
					continue
				}
				pos++
				t.RawSet(float64(pos), items[i](f))
			}
			return t
		}
	case *eUnop:
		inner := fs.expr(x.e)
		desc := fs.describe(x.e)
		line := x.line
		switch x.op {
		case "not":
			return func(f *frame) Value { return !Truthy(inner(f)) }
		case "-":
			return func(f *frame) Value {
				v := inner(f)
				if n, ok := v.(float64); ok {
					return -n
				}
				if n, ok := ToNumber(v); ok {
					return -n
				}
				f.L.line = line
				if t, ok := v.(*Table); ok && t.Meta != nil {
					if h := t.Meta.RawGet("__unm"); h != nil {
						r := f.L.call(h, []Value{v}, "metamethod")
						if len(r) > 0 {
							return r[0]
						}
						return nil
					}
				}
				f.L.RuntimeError("attempt to perform arithmetic on %s", what(desc, v))
				return nil
			}
		case "#":
			return func(f *frame) Value {
				f.L.line = line
				return f.L.length(inner(f), desc)
			}
		}
	case *eBinop:
		l := fs.expr(x.l)
		r := fs.expr(x.r)
		dl, dr := fs.describe(x.l), fs.describe(x.r)
		line := x.line
		switch x.op {
		case "and":
			return func(f *frame) Value {
				a := l(f)
				if !Truthy(a) {
					return a
				}
				return r(f)
			}
		case "or":
			return func(f *frame) Value {
				a := l(f)
				if Truthy(a) {
					return a
				}
				return r(f)
			}
		case "+", "-", "*", "/", "%", "^":
			op := x.op[0]
			return func(f *frame) Value {
				a, b := l(f), r(f)
				if x1, ok := a.(float64); ok {
					if y1, ok := b.(float64); ok {
						switch op {
						case '+':
							return x1 + y1
						case '-':
							return x1 - y1
						case '*':
							return x1 * y1
						}
					}
				}
				f.L.line = line
				return f.L.arith(op, a, b, dl, dr)
			}
		case "..":
			return func(f *frame) Value {
				a, b := l(f), r(f)
				if s1, ok := a.(string); ok {
					if s2, ok := b.(string); ok {
						return s1 + s2
					}
				}
				f.L.line = line
				return f.L.concat(a, b, dl, dr)
			}
		case "==":
			return func(f *frame) Value { return f.L.equal(l(f), r(f)) }
		case "~=":
			return func(f *frame) Value { return !f.L.equal(l(f), r(f)) }
		case "<":
			return func(f *frame) Value {
				a, b := l(f), r(f)
				f.L.line = line
				return f.L.lessThan(a, b)
			}
		case "<=":
			return func(f *frame) Value {
				a, b := l(f), r(f)
				f.L.line = line
				return f.L.lessEqual(a, b)
			}
		case ">":
			return func(f *frame) Value {
				a, b := l(f), r(f)
				f.L.line = line
				return f.L.lessThan(b, a)
			}
		case ">=":
			return func(f *frame) Value {
				a, b := l(f), r(f)
				f.L.line = line
				return f.L.lessEqual(b, a)
			}
		}
	}
	panic(fmt.Sprintf("lua: unknown expression %T", e))
}
