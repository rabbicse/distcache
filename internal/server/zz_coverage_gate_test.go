package server_test

// This file is named zz_* on purpose: Go runs test files in alphabetical
// order (and tests in source order within a file), so TestZZCoverageGate runs
// after every other test in the package has recorded its commands through
// testutil.RunScript/Differential. Do not rely on the test name for ordering.
//
// IMPORTANT: no test in package server_test may call t.Parallel. Parallel
// tests are paused until the serial tests finish, so they would run after the
// gate and their commands would be reported as uncovered. (Differential tests
// also share one external Redis.)

import (
	"flag"
	"slices"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestZZCoverageGate fails listing every registered command that no script
// exercised. It only makes sense for a whole-package run, so it is skipped
// when -run is given.
func TestZZCoverageGate(t *testing.T) {
	if f := flag.Lookup("test.run"); f != nil && f.Value.String() != "" {
		t.Skip("coverage gate runs only for a whole-package run (no -run filter)")
	}
	s, _ := testutil.StartServer(t, nil)
	covered := testutil.CoveredCommands()
	var missing []string
	for _, n := range s.CommandNames() {
		if !slices.Contains(covered, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d commands never exercised: %v", len(missing), missing)
	}
}
