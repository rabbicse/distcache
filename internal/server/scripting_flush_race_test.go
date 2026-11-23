// Regression test: SCRIPT FLUSH must not race with scripts that are taking
// interpreters from, or returning them to, the VM pool.
package server_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestScriptFlush_ConcurrentWithEvalRO runs read-only scripts in parallel
// with SCRIPT FLUSH. sync.Pool hides its internals from the race detector,
// but an unsynchronised pool swap crashes the process with SIGSEGV.
func TestScriptFlush_ConcurrentWithEvalRO(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	const rounds = 10000
	var wg sync.WaitGroup
	for range 4 {
		c := testutil.MustDial(t, addr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if got := c.DoS("EVAL_RO", "return 1", "0"); got != ":1" {
					t.Errorf("EVAL_RO = %s, want :1", got)
					return
				}
			}
		}()
	}
	f := testutil.MustDial(t, addr)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			if got := f.DoS("SCRIPT", "FLUSH"); got != "+OK" {
				t.Errorf("SCRIPT FLUSH = %s, want +OK", got)
				return
			}
		}
	}()
	wg.Wait()
	if got := f.DoS("EVALSHA", "e0e1f9fabfc9d4800c877a703b823ac0578ff8db", "0"); !strings.HasPrefix(got, "-NOSCRIPT") && got != ":1" {
		t.Errorf("EVALSHA after flush = %s, want NOSCRIPT or :1", got)
	}
}
