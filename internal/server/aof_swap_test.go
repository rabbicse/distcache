// Tests that swapping in a rewritten AOF, which closes and reopens the file
// around the rename, neither loses nor duplicates concurrent writes.
package server_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

func TestAOF_RewriteSwapKeepsConcurrentWrites(t *testing.T) {
	const writers, incrs = 4, 500
	dir := t.TempDir()
	s, c := startAOFServer(t, dir)
	addr := s.Addr()
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := testutil.MustDial(t, addr)
			for range incrs {
				w.DoS("INCR", "n")
			}
		}()
	}
	for range 5 {
		c.DoS("BGREWRITEAOF")
		waitInfoField(t, c, "persistence", "aof_rewrite_in_progress:0")
	}
	wg.Wait()
	if err := s.Shutdown(false); err != nil {
		t.Fatal(err)
	}
	_, c = startAOFServer(t, dir)
	if got, want := c.DoS("GET", "n"), strconv.Quote(strconv.Itoa(writers*incrs)); got != want {
		t.Errorf("GET n after restart: got %s, want %s", got, want)
	}
}
