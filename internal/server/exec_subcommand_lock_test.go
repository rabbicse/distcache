// Regression tests: commands that address their key through a subcommand
// (MEMORY USAGE, OBJECT, XINFO) must lock that key's shard inside MULTI/EXEC.
// DEBUG OBJECT is the same kind of command but cannot be queued in MULTI.
package server_test

import (
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// TestExec_SubcommandKeyIsLocked runs MULTI/<subcommand key read>/EXEC next to
// writers on the same shard; without the shard lock -race (or the Go runtime's
// map check) reports a concurrent map read and write.
func TestExec_SubcommandKeyIsLocked(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
	}{
		{"memory usage", []string{"MEMORY", "USAGE", "k1"}},
		{"object encoding", []string{"OBJECT", "ENCODING", "k1"}},
		{"xinfo stream", []string{"XINFO", "STREAM", "k1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := testutil.StartServer(t, func(c *config.Config) { c.Shards = 1 })
			const rounds = 300
			var wg sync.WaitGroup
			run := func(f func(c *testutil.Client)) {
				c := testutil.MustDial(t, addr)
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range rounds {
						f(c)
					}
				}()
			}
			for range 2 {
				run(func(c *testutil.Client) {
					c.Do("SET", "k1", "v")
					c.Do("SET", "k2", "v")
					c.Do("DEL", "k1", "k2")
				})
				run(func(c *testutil.Client) {
					c.Do("MULTI")
					c.Do(tc.cmd...)
					c.Do("EXEC")
				})
			}
			wg.Wait()
			c := testutil.MustDial(t, addr)
			if got := c.DoS("PING"); got != "+PONG" {
				t.Fatalf("got %s, want +PONG", got)
			}
		})
	}
}
