// Package lua is a compact, dependency-free Lua 5.1 interpreter used by
// DistCache to run EVAL scripts and FUNCTION libraries, mirroring the Lua
// environment Redis embeds (base, string, table, math, bit, cjson, cmsgpack).
//
// Scripts are parsed into an AST and compiled into Go closures, which keeps
// the implementation small while running typical Redis scripts fast.
package lua

import (
	"fmt"
	"strconv"
	"strings"
)

type tokKind int

const (
	tEOF tokKind = iota
	tName
	tNumber
	tString
	tOp // punctuation / operators, value in s
	tKeyword
)

type token struct {
	kind tokKind
	s    string
	num  float64
	line int
}

var keywords = map[string]bool{
	"and": true, "break": true, "do": true, "else": true, "elseif": true, "end": true,
	"false": true, "for": true, "function": true, "if": true, "in": true, "local": true,
	"nil": true, "not": true, "or": true, "repeat": true, "return": true, "then": true,
	"true": true, "until": true, "while": true,
}

type lexer struct {
	src   string
	pos   int
	line  int
	chunk string
}

// SyntaxError is a compile error.
type SyntaxError struct{ Msg string }

func (e *SyntaxError) Error() string { return e.Msg }

func (lx *lexer) errf(line int, near string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if near != "" {
		msg += " near '" + near + "'"
	}
	panic(&SyntaxError{Msg: fmt.Sprintf("%s:%d: %s", lx.chunk, line, msg)})
}

func isAlpha(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (lx *lexer) peekc(o int) byte {
	if lx.pos+o < len(lx.src) {
		return lx.src[lx.pos+o]
	}
	return 0
}

// longBracket checks for [=*[ at pos and returns the level, or -1.
func (lx *lexer) longBracket() int {
	if lx.peekc(0) != '[' {
		return -1
	}
	i := 1
	for lx.peekc(i) == '=' {
		i++
	}
	if lx.peekc(i) == '[' {
		return i - 1
	}
	return -1
}

func (lx *lexer) readLong(level int) string {
	lx.pos += level + 2
	if lx.peekc(0) == '\r' {
		lx.pos++
		if lx.peekc(0) == '\n' {
			lx.pos++
		}
		lx.line++
	} else if lx.peekc(0) == '\n' {
		lx.pos++
		if lx.peekc(0) == '\r' {
			lx.pos++
		}
		lx.line++
	}
	closing := "]" + strings.Repeat("=", level) + "]"
	start := lx.pos
	idx := strings.Index(lx.src[lx.pos:], closing)
	if idx < 0 {
		lx.errf(lx.line, "<eof>", "unfinished long string")
	}
	s := lx.src[start : start+idx]
	lx.line += strings.Count(s, "\n")
	lx.pos = start + idx + len(closing)
	return s
}

func (lx *lexer) skipSpace() {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '\n':
			lx.line++
			lx.pos++
		case c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f':
			lx.pos++
		case c == '-' && lx.peekc(1) == '-':
			lx.pos += 2
			if lvl := lx.longBracket(); lvl >= 0 {
				lx.readLong(lvl)
				continue
			}
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '\n' {
				lx.pos++
			}
		default:
			return
		}
	}
}

func (lx *lexer) next() token {
	lx.skipSpace()
	if lx.pos >= len(lx.src) {
		return token{kind: tEOF, line: lx.line}
	}
	c := lx.src[lx.pos]
	line := lx.line
	switch {
	case isAlpha(c):
		start := lx.pos
		for lx.pos < len(lx.src) && (isAlpha(lx.src[lx.pos]) || isDigit(lx.src[lx.pos])) {
			lx.pos++
		}
		w := lx.src[start:lx.pos]
		if keywords[w] {
			return token{kind: tKeyword, s: w, line: line}
		}
		return token{kind: tName, s: w, line: line}
	case isDigit(c) || (c == '.' && isDigit(lx.peekc(1))):
		start := lx.pos
		if c == '0' && (lx.peekc(1) == 'x' || lx.peekc(1) == 'X') {
			lx.pos += 2
			for lx.pos < len(lx.src) && isHexDigit(lx.src[lx.pos]) {
				lx.pos++
			}
		} else {
			for lx.pos < len(lx.src) {
				ch := lx.src[lx.pos]
				if isDigit(ch) || ch == '.' {
					lx.pos++
				} else if ch == 'e' || ch == 'E' {
					lx.pos++
					if lx.peekc(0) == '+' || lx.peekc(0) == '-' {
						lx.pos++
					}
				} else {
					break
				}
			}
			for lx.pos < len(lx.src) && (isAlpha(lx.src[lx.pos]) || isDigit(lx.src[lx.pos])) {
				lx.pos++
			}
		}
		txt := lx.src[start:lx.pos]
		f, ok := str2number(txt)
		if !ok {
			lx.errf(line, txt, "malformed number")
		}
		return token{kind: tNumber, num: f, s: txt, line: line}
	case c == '"' || c == '\'':
		return token{kind: tString, s: lx.readString(c), line: line}
	case c == '[':
		if lvl := lx.longBracket(); lvl >= 0 {
			return token{kind: tString, s: lx.readLong(lvl), line: line}
		}
	}
	for _, op := range []string{"...", "..", "==", "~=", "<=", ">="} {
		if strings.HasPrefix(lx.src[lx.pos:], op) {
			lx.pos += len(op)
			return token{kind: tOp, s: op, line: line}
		}
	}
	if strings.IndexByte("+-*/%^#<>=(){}[];:,.", c) >= 0 {
		lx.pos++
		return token{kind: tOp, s: string(c), line: line}
	}
	lx.errf(line, string(c), "unexpected symbol")
	return token{}
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (lx *lexer) readString(q byte) string {
	lx.pos++
	var b strings.Builder
	for {
		if lx.pos >= len(lx.src) {
			lx.errf(lx.line, "<eof>", "unfinished string")
		}
		c := lx.src[lx.pos]
		switch c {
		case q:
			lx.pos++
			return b.String()
		case '\n':
			lx.errf(lx.line, b.String(), "unfinished string")
		case '\\':
			lx.pos++
			if lx.pos >= len(lx.src) {
				lx.errf(lx.line, "<eof>", "unfinished string")
			}
			e := lx.src[lx.pos]
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '\\', '"', '\'':
				b.WriteByte(e)
			case '\n':
				b.WriteByte('\n')
				lx.line++
			default:
				if isDigit(e) {
					n := 0
					i := 0
					for i < 3 && lx.pos < len(lx.src) && isDigit(lx.src[lx.pos]) {
						n = n*10 + int(lx.src[lx.pos]-'0')
						lx.pos++
						i++
					}
					if n > 255 {
						lx.errf(lx.line, "", "escape sequence too large")
					}
					b.WriteByte(byte(n))
					continue
				}
				b.WriteByte(e)
			}
			lx.pos++
		default:
			b.WriteByte(c)
			lx.pos++
		}
	}
}

// str2number converts Lua numerals (decimal, hex) like lua_str2number.
func str2number(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := false
	t := s
	if t[0] == '-' || t[0] == '+' {
		neg = t[0] == '-'
		t = t[1:]
	}
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		u, err := strconv.ParseUint(t[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		f := float64(u)
		if neg {
			f = -f
		}
		return f, true
	}
	if strings.ContainsAny(t, "xXpP_") || strings.EqualFold(t, "inf") || strings.EqualFold(t, "infinity") || strings.EqualFold(t, "nan") {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}
