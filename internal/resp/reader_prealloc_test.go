// Tests that ReadCommand's memory use tracks the bytes received rather than
// the lengths a client declares.
package resp

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// allocatedBy returns how many bytes fn allocated on the heap.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestReadCommand_DeclaredLengths_NotPreallocated(t *testing.T) {
	const limit = 1 << 20
	tests := []struct {
		name  string
		input string
	}{
		{"HugeBulk", "*1\r\n$536870000\r\nab"},
		{"HugeMultibulk", "*1048576\r\n$1\r\na\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.input), 0)
			got := allocatedBy(func() {
				if _, err := r.ReadCommand(); err == nil {
					t.Error("ReadCommand on truncated input: got nil error, want EOF")
				}
			})
			if got > limit {
				t.Errorf("allocated %d bytes for %d input bytes, want at most %d", got, len(tc.input), limit)
			}
		})
	}
}

func TestReadCommand_LargeBulk_ReadInChunks(t *testing.T) {
	val := bytes.Repeat([]byte("x"), 3*maxPrealloc+7)
	in := AppendCommandStrings(nil, "SET", "k", string(val))
	r := NewReader(bytes.NewReader(in), 0)
	args, err := r.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || !bytes.Equal(args[2], val) {
		t.Errorf("got %d args, value length %d; want 3 args, value length %d", len(args), len(args[len(args)-1]), len(val))
	}
}
