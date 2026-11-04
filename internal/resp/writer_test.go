package resp

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

func TestWriter_Truncate(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want string
	}{
		{"shrinks", 5, "+OK\r\n"},
		{"to zero", 0, ""},
		{"larger is noop", 99, "+OK\r\n:1\r\n"},
		{"negative is noop", -1, "+OK\r\n:1\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWriter(nil, 2)
			w.WriteOK()
			w.WriteInt(1)
			w.Truncate(tc.n)
			if got := string(w.Bytes()); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriter_Flushes(t *testing.T) {
	var sink []byte
	w := NewWriter(writerFunc(func(p []byte) (int, error) { sink = append(sink, p...); return len(p), nil }), 2)
	w.WriteOK()
	if w.Flushes() != 0 {
		t.Fatalf("got %d flushes, want 0", w.Flushes())
	}
	_ = w.Flush()
	_ = w.Flush() // empty: not counted
	if w.Flushes() != 1 || string(sink) != "+OK\r\n" {
		t.Errorf("got %d flushes %q, want 1 %q", w.Flushes(), sink, "+OK\r\n")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestWriter_RESP2vsRESP3(t *testing.T) {
	tests := []struct {
		name  string
		write func(w *Writer)
		want2 string
		want3 string
	}{
		{"null", func(w *Writer) { w.WriteNull() }, "$-1\r\n", "_\r\n"},
		{"null array", func(w *Writer) { w.WriteNullArray() }, "*-1\r\n", "_\r\n"},
		{"map", func(w *Writer) { w.WriteMapLen(2) }, "*4\r\n", "%2\r\n"},
		{"set", func(w *Writer) { w.WriteSetLen(3) }, "*3\r\n", "~3\r\n"},
		{"push", func(w *Writer) { w.WritePushLen(3) }, "*3\r\n", ">3\r\n"},
		{"double", func(w *Writer) { w.WriteDouble(1.5) }, "$3\r\n1.5\r\n", ",1.5\r\n"},
		{"double inf", func(w *Writer) { w.WriteDouble(math.Inf(1)) }, "$3\r\ninf\r\n", ",inf\r\n"},
		{"bool true", func(w *Writer) { w.WriteBool(true) }, ":1\r\n", "#t\r\n"},
		{"bool false", func(w *Writer) { w.WriteBool(false) }, ":0\r\n", "#f\r\n"},
		{"verbatim", func(w *Writer) { w.WriteVerbatim("abcd", "txt") }, "$4\r\nabcd\r\n", "=8\r\ntxt:abcd\r\n"},
	}
	for _, tc := range tests {
		for proto, want := range map[int]string{2: tc.want2, 3: tc.want3} {
			t.Run(fmt.Sprintf("%s/RESP%d", tc.name, proto), func(t *testing.T) {
				w := NewWriter(nil, proto)
				tc.write(w)
				if got := string(w.Bytes()); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
			})
		}
	}
}

func TestWriter_BasicReplies(t *testing.T) {
	tests := []struct {
		name  string
		write func(w *Writer)
		want  string
	}{
		{"simple", func(w *Writer) { w.WriteSimple("PONG") }, "+PONG\r\n"},
		{"ok", func(w *Writer) { w.WriteOK() }, "+OK\r\n"},
		{"error", func(w *Writer) { w.WriteError("ERR bad") }, "-ERR bad\r\n"},
		{"error newlines", func(w *Writer) { w.WriteError("ERR a\r\nb") }, "-ERR a  b\r\n"},
		{"int", func(w *Writer) { w.WriteInt(-3) }, ":-3\r\n"},
		{"bulk", func(w *Writer) { w.WriteBulk([]byte("ab")) }, "$2\r\nab\r\n"},
		{"bulk string", func(w *Writer) { w.WriteBulkString("abc") }, "$3\r\nabc\r\n"},
		{"bulk int", func(w *Writer) { w.WriteBulkInt(12) }, "$2\r\n12\r\n"},
		{"array len", func(w *Writer) { w.WriteArrayLen(2) }, "*2\r\n"},
		{"raw", func(w *Writer) { w.WriteRaw([]byte("+x\r\n")) }, "+x\r\n"},
		{"bulks", func(w *Writer) { w.WriteBulks([][]byte{[]byte("a"), nil}) }, "*2\r\n$1\r\na\r\n$0\r\n\r\n"},
		{"strings", func(w *Writer) { w.WriteStrings([]string{"a", "bc"}) }, "*2\r\n$1\r\na\r\n$2\r\nbc\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWriter(nil, 2)
			tc.write(w)
			if got := string(w.Bytes()); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if w.Len() != len(tc.want) {
				t.Errorf("got Len %d, want %d", w.Len(), len(tc.want))
			}
			w.Reset()
			if w.Len() != 0 {
				t.Errorf("got Len %d after Reset, want 0", w.Len())
			}
		})
	}
}

func TestFormatDouble(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1, "1"},
		{0, "0"},
		{math.Copysign(0, -1), "-0"},
		{-3, "-3"},
		{1.5, "1.5"},
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
		{math.NaN(), "nan"},
		{1e21, "1e+21"},
		{1e-5, "1e-05"},
		{0.0001, "0.0001"},
		{sumTenthsAndTwoTenths(), "0.30000000000000004"},
	}
	for _, tc := range tests {
		if got := FormatDouble(tc.in); got != tc.want {
			t.Errorf("FormatDouble(%v): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAppendCommandStrings_RoundTrip(t *testing.T) {
	args := []string{"SET", "key", "", "va\r\nlue", "\x00bin"}
	enc := AppendCommandStrings(nil, args...)
	got, err := NewReader(bytes.NewReader(enc), 0).ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(args) {
		t.Fatalf("got %d args, want %d", len(got), len(args))
	}
	for i := range args {
		if string(got[i]) != args[i] {
			t.Errorf("arg %d: got %q, want %q", i, got[i], args[i])
		}
	}
}

func TestWriter_AutoFlushThreshold(t *testing.T) {
	var sink bytes.Buffer
	w := NewWriter(&sink, 2)
	w.WriteBulk(make([]byte, flushThreshold))
	if w.Flushes() != 1 || w.Len() != 0 {
		t.Errorf("got %d flushes, Len %d, want 1, 0", w.Flushes(), w.Len())
	}
}

func TestWriter_LockedSection(t *testing.T) {
	var mu sync.Mutex
	var sink bytes.Buffer
	w := NewWriter(&sink, 2)
	w.Mu = &mu

	mu.Lock()
	w.SetLockedSection(true)
	w.WriteBulk(make([]byte, flushThreshold)) // would auto-flush and deadlock on Mu
	if w.Flushes() != 0 || w.Len() < flushThreshold {
		t.Errorf("got %d flushes, Len %d, want 0 flushes and buffered data", w.Flushes(), w.Len())
	}
	if err := w.FlushLocked(); err != nil {
		t.Fatal(err)
	}
	w.SetLockedSection(false)
	mu.Unlock()

	if w.Flushes() != 1 || w.Len() != 0 || sink.Len() < flushThreshold {
		t.Errorf("got %d flushes, Len %d, sink %d, want 1, 0, full", w.Flushes(), w.Len(), sink.Len())
	}
}

func TestWriter_FlushError(t *testing.T) {
	errBoom := errors.New("boom")
	calls := 0
	w := NewWriter(writerFunc(func([]byte) (int, error) { calls++; return 0, errBoom }), 2)
	w.WriteOK()
	if err := w.Flush(); !errors.Is(err, errBoom) {
		t.Fatalf("got %v, want %v", err, errBoom)
	}
	w.WriteOK()
	if err := w.Flush(); !errors.Is(err, errBoom) || calls != 1 {
		t.Errorf("got %v after %d writes, want sticky error and 1 write", err, calls)
	}
	if !errors.Is(w.Err(), errBoom) {
		t.Errorf("got Err %v, want %v", w.Err(), errBoom)
	}
}

func TestWriter_FlushNilOut(t *testing.T) {
	w := NewWriter(nil, 2)
	w.WriteOK()
	if err := w.Flush(); err != nil || w.Len() != 5 {
		t.Errorf("got %v, Len %d, want nil, 5", err, w.Len())
	}
}

// sumTenthsAndTwoTenths adds at run time; the constant expression 0.1+0.2
// would be folded exactly to 0.3 by the compiler.
func sumTenthsAndTwoTenths() float64 {
	a, b := 0.1, 0.2
	return a + b
}
