package resp

import (
	"io"
	"math"
	"strconv"
	"strings"
)

var (
	posInf = math.Inf(1)
	negInf = math.Inf(-1)
)

// Writer encodes replies into an internal buffer. When Out is set the buffer
// is flushed to it once it grows beyond flushThreshold or on Flush. With a nil
// Out the writer acts as an in-memory capture (used by EXEC, Lua, MONITOR).
//
// Proto selects the protocol version (2 or 3). RESP3-only types are mapped to
// their RESP2 equivalents exactly as Redis does.
type Writer struct {
	Out   io.Writer
	Proto int
	buf   []byte
	err   error
}

const flushThreshold = 64 * 1024

// NewWriter creates a writer targeting out.
func NewWriter(out io.Writer, proto int) *Writer {
	return &Writer{Out: out, Proto: proto, buf: make([]byte, 0, 4096)}
}

// Bytes returns the buffered (unflushed) data.
func (w *Writer) Bytes() []byte { return w.buf }

// Reset discards buffered data.
func (w *Writer) Reset() { w.buf = w.buf[:0] }

// Len returns the number of buffered bytes.
func (w *Writer) Len() int { return len(w.buf) }

// Err returns the first write error.
func (w *Writer) Err() error { return w.err }

// Flush writes buffered bytes to Out.
func (w *Writer) Flush() error {
	if w.Out == nil || len(w.buf) == 0 {
		return w.err
	}
	if w.err == nil {
		_, w.err = w.Out.Write(w.buf)
	}
	if cap(w.buf) > 1<<20 {
		w.buf = make([]byte, 0, 4096)
	} else {
		w.buf = w.buf[:0]
	}
	return w.err
}

func (w *Writer) maybeFlush() {
	if w.Out != nil && len(w.buf) >= flushThreshold {
		_ = w.Flush()
	}
}

func (w *Writer) header(t byte, n int64) {
	w.buf = append(w.buf, t)
	w.buf = strconv.AppendInt(w.buf, n, 10)
	w.buf = append(w.buf, '\r', '\n')
}

// WriteRaw appends pre-encoded RESP.
func (w *Writer) WriteRaw(b []byte) { w.buf = append(w.buf, b...); w.maybeFlush() }

// WriteSimple writes a +status reply.
func (w *Writer) WriteSimple(s string) {
	w.buf = append(w.buf, '+')
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, '\r', '\n')
}

// WriteOK writes +OK.
func (w *Writer) WriteOK() { w.buf = append(w.buf, "+OK\r\n"...) }

// WriteError writes an error reply. msg must contain the error code prefix
// (e.g. "ERR ..." or "WRONGTYPE ..."); newlines are replaced by spaces.
func (w *Writer) WriteError(msg string) {
	if strings.ContainsAny(msg, "\r\n") {
		msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	}
	w.buf = append(w.buf, '-')
	w.buf = append(w.buf, msg...)
	w.buf = append(w.buf, '\r', '\n')
}

// WriteInt writes an integer reply.
func (w *Writer) WriteInt(n int64) { w.header(':', n) }

// WriteBulk writes a bulk string.
func (w *Writer) WriteBulk(b []byte) {
	w.header('$', int64(len(b)))
	w.buf = append(w.buf, b...)
	w.buf = append(w.buf, '\r', '\n')
	w.maybeFlush()
}

// WriteBulkString writes a bulk string from a Go string.
func (w *Writer) WriteBulkString(s string) {
	w.header('$', int64(len(s)))
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, '\r', '\n')
	w.maybeFlush()
}

// WriteBulkInt writes an integer as a bulk string.
func (w *Writer) WriteBulkInt(n int64) {
	var tmp [24]byte
	w.WriteBulk(strconv.AppendInt(tmp[:0], n, 10))
}

// WriteNull writes a null bulk ($-1 / _).
func (w *Writer) WriteNull() {
	if w.Proto >= 3 {
		w.buf = append(w.buf, "_\r\n"...)
	} else {
		w.buf = append(w.buf, "$-1\r\n"...)
	}
}

// WriteNullArray writes a null array (*-1 / _).
func (w *Writer) WriteNullArray() {
	if w.Proto >= 3 {
		w.buf = append(w.buf, "_\r\n"...)
	} else {
		w.buf = append(w.buf, "*-1\r\n"...)
	}
}

// WriteArrayLen writes an array header.
func (w *Writer) WriteArrayLen(n int) { w.header('*', int64(n)) }

// WriteMapLen writes a map header (RESP2: flat array of 2n).
func (w *Writer) WriteMapLen(n int) {
	if w.Proto >= 3 {
		w.header('%', int64(n))
	} else {
		w.header('*', int64(2*n))
	}
}

// WriteSetLen writes a set header (RESP2: array).
func (w *Writer) WriteSetLen(n int) {
	if w.Proto >= 3 {
		w.header('~', int64(n))
	} else {
		w.header('*', int64(n))
	}
}

// WritePushLen writes a push header (RESP2: array).
func (w *Writer) WritePushLen(n int) {
	if w.Proto >= 3 {
		w.header('>', int64(n))
	} else {
		w.header('*', int64(n))
	}
}

// WriteDouble writes a double (RESP2: bulk string).
func (w *Writer) WriteDouble(f float64) {
	s := FormatDouble(f)
	if w.Proto >= 3 {
		w.buf = append(w.buf, ',')
		w.buf = append(w.buf, s...)
		w.buf = append(w.buf, '\r', '\n')
		return
	}
	w.WriteBulkString(s)
}

// WriteBool writes a boolean (RESP2: integer 1/0).
func (w *Writer) WriteBool(b bool) {
	if w.Proto >= 3 {
		if b {
			w.buf = append(w.buf, "#t\r\n"...)
		} else {
			w.buf = append(w.buf, "#f\r\n"...)
		}
		return
	}
	if b {
		w.WriteInt(1)
	} else {
		w.WriteInt(0)
	}
}

// WriteVerbatim writes a verbatim string (RESP2: bulk string).
func (w *Writer) WriteVerbatim(s, format string) {
	if w.Proto >= 3 {
		w.header('=', int64(len(s)+4))
		w.buf = append(w.buf, format...)
		w.buf = append(w.buf, ':')
		w.buf = append(w.buf, s...)
		w.buf = append(w.buf, '\r', '\n')
		return
	}
	w.WriteBulkString(s)
}

// WriteBulks writes an array of bulk strings.
func (w *Writer) WriteBulks(items [][]byte) {
	w.WriteArrayLen(len(items))
	for _, it := range items {
		w.WriteBulk(it)
	}
}

// WriteStrings writes an array of bulk strings.
func (w *Writer) WriteStrings(items []string) {
	w.WriteArrayLen(len(items))
	for _, it := range items {
		w.WriteBulkString(it)
	}
}

// FormatDouble formats a float the way Redis 7.2+ does: the shortest
// representation that round-trips, using exponent notation like printf %.17g.
func FormatDouble(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "nan"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e17 {
		if f == 0 && math.Signbit(f) {
			return "-0"
		}
		return strconv.FormatInt(int64(f), 10)
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddddde±XX
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	if exp < -4 || exp >= 17 {
		// printf style exponent: at least two digits.
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		es := strconv.Itoa(exp)
		if len(es) < 2 {
			es = "0" + es
		}
		return mant + "e" + sign + es
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// AppendCommand encodes args as a RESP multi-bulk request.
func AppendCommand(dst []byte, args ...[]byte) []byte {
	dst = append(dst, '*')
	dst = strconv.AppendInt(dst, int64(len(args)), 10)
	dst = append(dst, '\r', '\n')
	for _, a := range args {
		dst = append(dst, '$')
		dst = strconv.AppendInt(dst, int64(len(a)), 10)
		dst = append(dst, '\r', '\n')
		dst = append(dst, a...)
		dst = append(dst, '\r', '\n')
	}
	return dst
}

// AppendCommandStrings encodes string args as a RESP request.
func AppendCommandStrings(dst []byte, args ...string) []byte {
	b := make([][]byte, len(args))
	for i, a := range args {
		b[i] = []byte(a)
	}
	return AppendCommand(dst, b...)
}
