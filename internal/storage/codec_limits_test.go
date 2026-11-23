// This file tests that the decoder rejects forged length prefixes without
// allocating memory for them.
package storage

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"hash/crc64"
	"runtime"
	"testing"
)

// forgeDump wraps body in a valid DUMP version footer and checksum.
func forgeDump(body []byte) []byte {
	out := append(append([]byte(nil), body...), DumpVersion, 0)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], crc64.Checksum(out, crcTable))
	return append(out, b[:]...)
}

// cat concatenates byte slices.
func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func uv(v uint64) []byte { return binary.AppendUvarint(nil, v) }

// allocated reports the bytes allocated while fn runs.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestDecodeValue_ForgedLengthsDoNotAllocate(t *testing.T) {
	huge := uv(1 << 32)
	cases := []struct {
		name string
		body []byte
	}{
		{"string", cat([]byte{tString}, huge, []byte("x"))},
		{"list element", cat([]byte{tList}, uv(1), huge, []byte("x"))},
		{"list count", cat([]byte{tList}, huge, []byte{0})},
		{"stream fields", cat([]byte{tStream}, uv(1), []byte{0, 0}, uv(1<<24), []byte{0})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := forgeDump(tc.body)
			var err error
			got := allocated(func() { _, err = DecodeValue(p) })
			if err == nil {
				t.Fatalf("got nil error, want ErrBadSnapshot")
			}
			if got > 1<<20 {
				t.Fatalf("got %d bytes allocated, want < 1 MiB", got)
			}
		})
	}
}

func TestDecode_ForgedSnapshotLengthDoesNotAllocate(t *testing.T) {
	snap := cat([]byte(snapMagic), []byte{snapVersion, tString}, uv(1), []byte("k"), uv(1<<32), []byte("short"))
	var err error
	got := allocated(func() {
		err = Decode(bufio.NewReader(bytes.NewReader(snap)), func(int, string, any, int64) error { return nil })
	})
	if err == nil {
		t.Fatalf("got nil error, want a decode error")
	}
	if got > 4<<20 {
		t.Fatalf("got %d bytes allocated, want < 4 MiB", got)
	}
}
