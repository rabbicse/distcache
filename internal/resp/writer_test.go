package resp

import "testing"

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
