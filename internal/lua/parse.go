package lua

// ---- AST ----

type expr interface{}

type (
	eNil      struct{}
	eTrue     struct{}
	eFalse    struct{}
	eNumber   struct{ v float64 }
	eString   struct{ v string }
	eVararg   struct{}
	eFunction struct{ f *funcBody }
	eTable    struct {
		items []expr // positional
		keys  []expr // keyed: keys[i] => vals[i]
		vals  []expr
		order []int // >=0: keyed index, <0: -(positional index)-1
	}
	eBinop struct {
		op   string
		l, r expr
		line int
	}
	eUnop struct {
		op   string
		e    expr
		line int
	}
	eName struct {
		name string
		line int
	}
	eIndex struct {
		obj, key expr
		line     int
	}
	eCall struct {
		fn   expr
		args []expr
		line int
	}
	eMethod struct {
		obj  expr
		name string
		args []expr
		line int
	}
	eParen struct{ e expr }
)

type funcBody struct {
	params   []string
	vararg   bool
	body     []stmt
	name     string
	line     int
	isMethod bool
}

type stmt interface{}

type (
	sLocal struct {
		names []string
		exprs []expr
		line  int
	}
	sAssign struct {
		targets []expr
		exprs   []expr
		line    int
	}
	sCall struct {
		call expr
		line int
	}
	sDo    struct{ body []stmt }
	sWhile struct {
		cond expr
		body []stmt
		line int
	}
	sRepeat struct {
		body []stmt
		cond expr
		line int
	}
	sIf struct {
		conds  []expr
		blocks [][]stmt
		els    []stmt
		line   int
	}
	sNumFor struct {
		v                  string
		start, limit, step expr
		body               []stmt
		line               int
	}
	sGenFor struct {
		names []string
		exprs []expr
		body  []stmt
		line  int
	}
	sFunction struct {
		path   []string
		method string
		f      *funcBody
		line   int
	}
	sLocalFunction struct {
		name string
		f    *funcBody
		line int
	}
	sReturn struct {
		exprs []expr
		line  int
	}
	sBreak struct{ line int }
)

// ---- parser ----

type parser struct {
	lx    *lexer
	tok   token
	ahead *token
}

func (p *parser) next() {
	if p.ahead != nil {
		p.tok = *p.ahead
		p.ahead = nil
		return
	}
	p.tok = p.lx.next()
}

func (p *parser) peek() token {
	if p.ahead == nil {
		t := p.lx.next()
		p.ahead = &t
	}
	return *p.ahead
}

func tokText(t token) string {
	switch t.kind {
	case tEOF:
		return "<eof>"
	case tString:
		return t.s
	}
	return t.s
}

func (p *parser) check(s string) bool {
	return (p.tok.kind == tOp || p.tok.kind == tKeyword) && p.tok.s == s
}

func (p *parser) expect(s string) {
	if !p.check(s) {
		p.lx.errf(p.tok.line, tokText(p.tok), "'%s' expected", s)
	}
	p.next()
}

func (p *parser) expectMatch(what, who string, line int) {
	if !p.check(what) {
		if line == p.tok.line {
			p.lx.errf(p.tok.line, tokText(p.tok), "'%s' expected", what)
		}
		p.lx.errf(p.tok.line, tokText(p.tok), "'%s' expected (to close '%s' at line %d)", what, who, line)
	}
	p.next()
}

func (p *parser) name() string {
	if p.tok.kind != tName {
		p.lx.errf(p.tok.line, tokText(p.tok), "<name> expected")
	}
	n := p.tok.s
	p.next()
	return n
}

func blockEnd(t token) bool {
	if t.kind == tEOF {
		return true
	}
	if t.kind == tKeyword {
		switch t.s {
		case "else", "elseif", "end", "until":
			return true
		}
	}
	return false
}

func (p *parser) block() []stmt {
	var out []stmt
	for !blockEnd(p.tok) {
		if p.check("return") {
			line := p.tok.line
			p.next()
			var es []expr
			if !blockEnd(p.tok) && !p.check(";") {
				es = p.exprList()
			}
			if p.check(";") {
				p.next()
			}
			out = append(out, &sReturn{exprs: es, line: line})
			if !blockEnd(p.tok) {
				p.lx.errf(p.tok.line, tokText(p.tok), "'end' expected")
			}
			break
		}
		if p.check("break") {
			line := p.tok.line
			p.next()
			out = append(out, &sBreak{line: line})
			if p.check(";") {
				p.next()
			}
			if !blockEnd(p.tok) {
				p.lx.errf(p.tok.line, tokText(p.tok), "'end' expected")
			}
			break
		}
		out = append(out, p.statement())
		if p.check(";") {
			p.next()
		}
	}
	return out
}

func (p *parser) statement() stmt {
	line := p.tok.line
	if p.tok.kind == tKeyword {
		switch p.tok.s {
		case "if":
			p.next()
			s := &sIf{line: line}
			s.conds = append(s.conds, p.expr())
			p.expect("then")
			s.blocks = append(s.blocks, p.block())
			for p.check("elseif") {
				p.next()
				s.conds = append(s.conds, p.expr())
				p.expect("then")
				s.blocks = append(s.blocks, p.block())
			}
			if p.check("else") {
				p.next()
				s.els = p.block()
			}
			p.expectMatch("end", "if", line)
			return s
		case "while":
			p.next()
			cond := p.expr()
			p.expect("do")
			body := p.block()
			p.expectMatch("end", "while", line)
			return &sWhile{cond: cond, body: body, line: line}
		case "do":
			p.next()
			body := p.block()
			p.expectMatch("end", "do", line)
			return &sDo{body: body}
		case "for":
			p.next()
			n1 := p.name()
			if p.check("=") {
				p.next()
				s := &sNumFor{v: n1, line: line}
				s.start = p.expr()
				p.expect(",")
				s.limit = p.expr()
				if p.check(",") {
					p.next()
					s.step = p.expr()
				}
				p.expect("do")
				s.body = p.block()
				p.expectMatch("end", "for", line)
				return s
			}
			names := []string{n1}
			for p.check(",") {
				p.next()
				names = append(names, p.name())
			}
			p.expect("in")
			es := p.exprList()
			p.expect("do")
			body := p.block()
			p.expectMatch("end", "for", line)
			return &sGenFor{names: names, exprs: es, body: body, line: line}
		case "repeat":
			p.next()
			body := p.block()
			p.expectMatch("until", "repeat", line)
			cond := p.expr()
			return &sRepeat{body: body, cond: cond, line: line}
		case "function":
			p.next()
			path := []string{p.name()}
			method := ""
			for p.check(".") {
				p.next()
				path = append(path, p.name())
			}
			if p.check(":") {
				p.next()
				method = p.name()
			}
			fb := p.funcBody(line, method != "")
			fb.name = path[len(path)-1]
			if method != "" {
				fb.name = method
			}
			return &sFunction{path: path, method: method, f: fb, line: line}
		case "local":
			p.next()
			if p.check("function") {
				p.next()
				n := p.name()
				fb := p.funcBody(line, false)
				fb.name = n
				return &sLocalFunction{name: n, f: fb, line: line}
			}
			names := []string{p.name()}
			for p.check(",") {
				p.next()
				names = append(names, p.name())
			}
			var es []expr
			if p.check("=") {
				p.next()
				es = p.exprList()
			}
			return &sLocal{names: names, exprs: es, line: line}
		}
	}
	// expression statement: call or assignment
	e := p.primaryExpr()
	if p.check("=") || p.check(",") {
		targets := []expr{e}
		for p.check(",") {
			p.next()
			targets = append(targets, p.primaryExpr())
		}
		p.expect("=")
		es := p.exprList()
		for _, t := range targets {
			switch t.(type) {
			case *eName, *eIndex:
			default:
				p.lx.errf(line, "", "syntax error")
			}
		}
		return &sAssign{targets: targets, exprs: es, line: line}
	}
	switch e.(type) {
	case *eCall, *eMethod:
		return &sCall{call: e, line: line}
	}
	p.lx.errf(p.tok.line, tokText(p.tok), "syntax error")
	return nil
}

func (p *parser) funcBody(line int, method bool) *funcBody {
	fb := &funcBody{line: line, isMethod: method}
	if method {
		fb.params = append(fb.params, "self")
	}
	p.expect("(")
	if !p.check(")") {
		for {
			if p.check("...") {
				p.next()
				fb.vararg = true
				break
			}
			fb.params = append(fb.params, p.name())
			if !p.check(",") {
				break
			}
			p.next()
		}
	}
	p.expect(")")
	fb.body = p.block()
	p.expectMatch("end", "function", line)
	return fb
}

func (p *parser) exprList() []expr {
	es := []expr{p.expr()}
	for p.check(",") {
		p.next()
		es = append(es, p.expr())
	}
	return es
}

var binPri = map[string][2]int{
	"+": {6, 6}, "-": {6, 6}, "*": {7, 7}, "/": {7, 7}, "%": {7, 7},
	"^": {10, 9}, "..": {5, 4},
	"==": {3, 3}, "~=": {3, 3}, "<": {3, 3}, "<=": {3, 3}, ">": {3, 3}, ">=": {3, 3},
	"and": {2, 2}, "or": {1, 1},
}

const unaryPri = 8

func (p *parser) expr() expr { return p.subexpr(0) }

func (p *parser) subexpr(limit int) expr {
	var e expr
	if (p.tok.kind == tKeyword && p.tok.s == "not") || (p.tok.kind == tOp && (p.tok.s == "-" || p.tok.s == "#")) {
		op := p.tok.s
		line := p.tok.line
		p.next()
		inner := p.subexpr(unaryPri)
		if n, ok := inner.(*eNumber); ok && op == "-" {
			e = &eNumber{v: -n.v}
		} else {
			e = &eUnop{op: op, e: inner, line: line}
		}
	} else {
		e = p.simpleExpr()
	}
	for {
		if p.tok.kind != tOp && p.tok.kind != tKeyword {
			break
		}
		pri, ok := binPri[p.tok.s]
		if !ok || pri[0] <= limit {
			break
		}
		op := p.tok.s
		line := p.tok.line
		p.next()
		r := p.subexpr(pri[1])
		e = &eBinop{op: op, l: e, r: r, line: line}
	}
	return e
}

func (p *parser) simpleExpr() expr {
	t := p.tok
	switch t.kind {
	case tNumber:
		p.next()
		return &eNumber{v: t.num}
	case tString:
		p.next()
		return &eString{v: t.s}
	case tKeyword:
		switch t.s {
		case "nil":
			p.next()
			return &eNil{}
		case "true":
			p.next()
			return &eTrue{}
		case "false":
			p.next()
			return &eFalse{}
		case "function":
			p.next()
			return &eFunction{f: p.funcBody(t.line, false)}
		}
	case tOp:
		switch t.s {
		case "...":
			p.next()
			return &eVararg{}
		case "{":
			return p.table()
		}
	}
	return p.primaryExpr()
}

func (p *parser) table() expr {
	line := p.tok.line
	p.expect("{")
	t := &eTable{}
	for !p.check("}") {
		if p.check("[") {
			p.next()
			k := p.expr()
			p.expect("]")
			p.expect("=")
			v := p.expr()
			t.order = append(t.order, len(t.keys))
			t.keys = append(t.keys, k)
			t.vals = append(t.vals, v)
		} else if p.tok.kind == tName && p.peek().kind == tOp && p.peek().s == "=" {
			k := p.tok.s
			p.next()
			p.next()
			v := p.expr()
			t.order = append(t.order, len(t.keys))
			t.keys = append(t.keys, &eString{v: k})
			t.vals = append(t.vals, v)
		} else {
			t.order = append(t.order, -len(t.items)-1)
			t.items = append(t.items, p.expr())
		}
		if p.check(",") || p.check(";") {
			p.next()
			continue
		}
		break
	}
	p.expectMatch("}", "{", line)
	return t
}

func (p *parser) primaryExpr() expr {
	var e expr
	line := p.tok.line
	switch {
	case p.tok.kind == tName:
		e = &eName{name: p.tok.s, line: line}
		p.next()
	case p.check("("):
		p.next()
		inner := p.expr()
		p.expectMatch(")", "(", line)
		e = &eParen{e: inner}
	default:
		p.lx.errf(p.tok.line, tokText(p.tok), "unexpected symbol")
	}
	for {
		line = p.tok.line
		switch {
		case p.check("."):
			p.next()
			n := p.name()
			e = &eIndex{obj: e, key: &eString{v: n}, line: line}
		case p.check("["):
			p.next()
			k := p.expr()
			p.expect("]")
			e = &eIndex{obj: e, key: k, line: line}
		case p.check(":"):
			p.next()
			n := p.name()
			args := p.callArgs()
			e = &eMethod{obj: e, name: n, args: args, line: line}
		case p.check("(") || p.check("{") || p.tok.kind == tString:
			args := p.callArgs()
			e = &eCall{fn: e, args: args, line: line}
		default:
			return e
		}
	}
}

func (p *parser) callArgs() []expr {
	switch {
	case p.tok.kind == tString:
		s := p.tok.s
		p.next()
		return []expr{&eString{v: s}}
	case p.check("{"):
		return []expr{p.table()}
	}
	line := p.tok.line
	p.expect("(")
	if p.check(")") {
		p.next()
		return nil
	}
	es := p.exprList()
	p.expectMatch(")", "(", line)
	return es
}

// parseChunk parses source into a main function body.
func parseChunk(src, chunk string) (fb *funcBody, err error) {
	defer func() {
		if r := recover(); r != nil {
			if se, ok := r.(*SyntaxError); ok {
				err = se
				return
			}
			panic(r)
		}
	}()
	p := &parser{lx: &lexer{src: src, line: 1, chunk: chunk}}
	p.next()
	body := p.block()
	if p.tok.kind != tEOF {
		p.lx.errf(p.tok.line, tokText(p.tok), "'<eof>' expected")
	}
	return &funcBody{vararg: true, body: body, name: "main chunk"}, nil
}
