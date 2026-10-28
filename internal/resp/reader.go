// Package resp implements the Redis Serialization Protocol (RESP2 and RESP3).
//
// The Reader parses client requests (multi-bulk and inline) and generic RESP
// values (used by replication and tests). The Writer encodes replies and
// transparently downgrades RESP3-only types when talking to a RESP2 client.
package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// ProtocolError is returned when the peer sends malformed data. The server
// replies with "-ERR Protocol error: <msg>" and closes the connection, exactly
// like Redis does.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string { return "Protocol error: " + e.Msg }

// Limits mirror Redis defaults (proto-max-bulk-len, multibulk length).
const (
	DefaultMaxBulkLen   = 512 * 1024 * 1024
	DefaultMaxMultiBulk = 1024 * 1024
	maxInlineLen        = 64 * 1024
)

// Reader reads RESP requests from a connection.
type Reader struct {
	br         *bufio.Reader
	MaxBulkLen int64
}

// NewReader wraps r with a buffered reader of the given size.
func NewReader(r io.Reader, size int) *Reader {
	if size <= 0 {
		size = 16 * 1024
	}
	return &Reader{br: bufio.NewReaderSize(r, size), MaxBulkLen: DefaultMaxBulkLen}
}

// Buffered returns the number of bytes that can be read without blocking.
// The server uses it to batch replies for pipelined requests.
func (r *Reader) Buffered() int { return r.br.Buffered() }

// readLine returns a line without the trailing CRLF. The returned slice is
// only valid until the next read.
func (r *Reader) readLine() ([]byte, error) {
	line, err := r.br.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		// Very long inline command: accumulate.
		buf := append([]byte(nil), line...)
		for err == bufio.ErrBufferFull {
			if len(buf) > maxInlineLen {
				return nil, &ProtocolError{"too big inline request"}
			}
			line, err = r.br.ReadSlice('\n')
			buf = append(buf, line...)
		}
		line = buf
	}
	if err != nil {
		return nil, err
	}
	n := len(line) - 1
	if n > 0 && line[n-1] == '\r' {
		n--
	}
	return line[:n], nil
}

// ReadCommand reads one request. Empty requests (blank inline lines, "*0")
// return a nil slice and nil error and should simply be skipped.
func (r *Reader) ReadCommand() ([][]byte, error) {
	b, err := r.br.ReadByte()
	if err != nil {
		return nil, err
	}
	if b != '*' {
		if err := r.br.UnreadByte(); err != nil {
			return nil, err
		}
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}
		args, ok := SplitArgs(string(line))
		if !ok {
			return nil, &ProtocolError{"unbalanced quotes in request"}
		}
		out := make([][]byte, len(args))
		for i, a := range args {
			out[i] = []byte(a)
		}
		return out, nil
	}
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	n, ok := parseInt(line)
	if !ok || n > DefaultMaxMultiBulk {
		return nil, &ProtocolError{"invalid multibulk length"}
	}
	if n <= 0 {
		return nil, nil
	}
	args := make([][]byte, n)
	for i := int64(0); i < n; i++ {
		b, err := r.br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != '$' {
			return nil, &ProtocolError{fmt.Sprintf("expected '$', got '%c'", b)}
		}
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}
		l, ok := parseInt(line)
		if !ok || l < 0 || l > r.MaxBulkLen {
			return nil, &ProtocolError{"invalid bulk length"}
		}
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(r.br, buf); err != nil {
			return nil, err
		}
		args[i] = buf[:l:l]
	}
	return args, nil
}

// Value is a decoded generic RESP value.
type Value struct {
	Type  byte // one of + - : $ * % ~ > , # _ = ( |
	Str   []byte
	Int   int64
	Float float64
	Null  bool
	Elems []Value // arrays, sets, pushes; maps hold k,v,k,v...
}

// ErrUnexpected is returned for unknown type markers.
var ErrUnexpected = errors.New("resp: unexpected type marker")

// ReadValue parses any RESP2/RESP3 value.
func (r *Reader) ReadValue() (Value, error) {
	t, err := r.br.ReadByte()
	if err != nil {
		return Value{}, err
	}
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	v := Value{Type: t}
	switch t {
	case '+', '-', '(':
		v.Str = append([]byte(nil), line...)
	case ':':
		n, ok := parseInt(line)
		if !ok {
			return v, &ProtocolError{"bad integer"}
		}
		v.Int = n
	case ',':
		f, err := ParseFloat(string(line))
		if err != nil {
			return v, &ProtocolError{"bad double"}
		}
		v.Float = f
		v.Str = append([]byte(nil), line...)
	case '#':
		if len(line) == 1 && line[0] == 't' {
			v.Int = 1
		}
	case '_':
		v.Null = true
	case '$', '=', '!':
		n, ok := parseInt(line)
		if !ok {
			return v, &ProtocolError{"bad bulk length"}
		}
		if n < 0 {
			v.Null = true
			return v, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r.br, buf); err != nil {
			return v, err
		}
		v.Str = buf[:n:n]
	case '*', '~', '>', '%', '|':
		n, ok := parseInt(line)
		if !ok {
			return v, &ProtocolError{"bad aggregate length"}
		}
		if n < 0 {
			v.Null = true
			return v, nil
		}
		if t == '%' || t == '|' {
			n *= 2
		}
		v.Elems = make([]Value, n)
		for i := range v.Elems {
			if v.Elems[i], err = r.ReadValue(); err != nil {
				return v, err
			}
		}
		if t == '|' { // attributes precede the real value; skip them
			return r.ReadValue()
		}
	default:
		return v, ErrUnexpected
	}
	return v, nil
}

// ReadRaw reads exactly n bytes (used for replication payloads).
func (r *Reader) ReadRaw(n int64) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r.br, buf)
	return buf, err
}

// ReadLine exposes line reads (used by replication handshake).
func (r *Reader) ReadLine() (string, error) {
	l, err := r.readLine()
	return string(l), err
}

// Underlying returns the buffered reader.
func (r *Reader) Underlying() *bufio.Reader { return r.br }

func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 20 {
		return 0, false
	}
	neg := false
	i := 0
	if b[0] == '-' {
		neg = true
		i = 1
		if len(b) == 1 {
			return 0, false
		}
	}
	var n int64
	for ; i < len(b); i++ {
		c := b[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

// ParseFloat parses doubles the way Redis does, accepting inf/-inf.
func ParseFloat(s string) (float64, error) {
	switch s {
	case "inf", "+inf", "Inf", "+Inf", "INF", "+INF":
		return posInf, nil
	case "-inf", "-Inf", "-INF":
		return negInf, nil
	}
	return strconv.ParseFloat(s, 64)
}

// SplitArgs splits an inline command line like sdssplitargs: it supports
// "double quoted" strings with escapes (\n \r \t \b \a \xHH) and 'single'
// quoted strings. ok is false on unbalanced quotes.
func SplitArgs(line string) ([]string, bool) {
	var out []string
	i := 0
	for {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return out, true
		}
		var cur []byte
		inq, insq, done := false, false, false
		for !done {
			if inq {
				if i >= len(line) {
					return nil, false
				}
				if line[i] == '\\' && i+3 < len(line) && line[i+1] == 'x' && isHex(line[i+2]) && isHex(line[i+3]) {
					cur = append(cur, hexVal(line[i+2])*16+hexVal(line[i+3]))
					i += 3
				} else if line[i] == '\\' && i+1 < len(line) {
					i++
					switch line[i] {
					case 'n':
						cur = append(cur, '\n')
					case 'r':
						cur = append(cur, '\r')
					case 't':
						cur = append(cur, '\t')
					case 'b':
						cur = append(cur, '\b')
					case 'a':
						cur = append(cur, '\a')
					default:
						cur = append(cur, line[i])
					}
				} else if line[i] == '"' {
					if i+1 < len(line) && !isSpace(line[i+1]) {
						return nil, false
					}
					done = true
				} else {
					cur = append(cur, line[i])
				}
			} else if insq {
				if i >= len(line) {
					return nil, false
				}
				if line[i] == '\\' && i+1 < len(line) && line[i+1] == '\'' {
					i++
					cur = append(cur, '\'')
				} else if line[i] == '\'' {
					if i+1 < len(line) && !isSpace(line[i+1]) {
						return nil, false
					}
					done = true
				} else {
					cur = append(cur, line[i])
				}
			} else {
				if i >= len(line) {
					done = true
					break
				}
				switch line[i] {
				case ' ', '\n', '\r', '\t', 0:
					done = true
				case '"':
					inq = true
				case '\'':
					insq = true
				default:
					cur = append(cur, line[i])
				}
			}
			if i < len(line) {
				i++
			}
		}
		out = append(out, string(cur))
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\v' || c == '\f'
}
func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
