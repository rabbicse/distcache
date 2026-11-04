package testutil

import "testing"

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		ver  string
		want bool
	}{
		{"8.4.0", true}, {"8.4", true}, {"8.10.1", true}, {"9.0.0", true},
		{"8.2.1", false}, {"7.9.9", false}, {"", false}, {"abc", false},
	}
	for _, tc := range tests {
		if got := versionAtLeast(tc.ver, 8, 4); got != tc.want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", tc.ver, got, tc.want)
		}
	}
}
