// Tests that memory values overflowing int64 are rejected.
package config

import (
	"strings"
	"testing"
)

func TestParseMemory_Overflow(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"9223372036854775807", 1<<63 - 1, false},
		{"8589934591gb", 8589934591 << 30, false},
		{"8589934592gb", 0, true},
		{"99999999999gb", 0, true},
		{"9223372036854775807k", 0, true},
		{"99999999999999999999", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseMemory(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseMemory(%q) = %d, %v; want %d, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestSet_MaxmemoryOverflow_Rejected(t *testing.T) {
	c := Default()
	err := c.Set("maxmemory", "99999999999gb", false)
	if err == nil || !strings.Contains(err.Error(), "argument must be a memory value") {
		t.Errorf("CONFIG SET maxmemory 99999999999gb: got %v, want a memory value error", err)
	}
	path := writeFile(t, t.TempDir(), "redis.conf", "maxmemory 99999999999gb\n")
	if err := Default().LoadFile(path); err == nil || !strings.Contains(err.Error(), "maxmemory") {
		t.Errorf("LoadFile with overflowing maxmemory: got %v, want an error naming maxmemory", err)
	}
}
