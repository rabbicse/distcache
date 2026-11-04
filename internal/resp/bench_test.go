// Microbenchmarks for command parsing and reply encoding.
package resp

import (
	"bytes"
	"io"
	"testing"
)

// cyclicReader replays buf forever so ReadCommand never hits EOF.
type cyclicReader struct {
	buf []byte
	off int
}

func (r *cyclicReader) Read(p []byte) (int, error) {
	n := copy(p, r.buf[r.off:])
	r.off = (r.off + n) % len(r.buf)
	return n, nil
}

func BenchmarkReadCommand(b *testing.B) {
	cmd := AppendCommandStrings(nil, "SET", "key:000000012345", "xxx")
	r := NewReader(&cyclicReader{buf: bytes.Repeat(cmd, 64)}, 16*1024)
	b.SetBytes(int64(len(cmd)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := r.ReadCommand(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteBulk(b *testing.B) {
	w := NewWriter(io.Discard, 2)
	val := []byte("xxx")
	b.ReportAllocs()
	for b.Loop() {
		w.WriteBulk(val)
		w.Reset()
	}
}
