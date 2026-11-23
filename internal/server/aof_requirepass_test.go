// Regression test: the append-only file must replay on a server that
// requires a password.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// TestAOF_ReplaysWithRequirePass writes through an authenticated client,
// restarts from the AOF and expects the data back. The loader's internal
// client used to count as unauthenticated, so every replayed command was
// refused with NOAUTH and the dataset came back empty.
func TestAOF_ReplaysWithRequirePass(t *testing.T) {
	dir := t.TempDir()
	opts := testutil.Options{Mutate: func(c *config.Config) {
		c.Dir = dir
		c.AppendOnly.Store(true)
		c.AppendFsync = "always"
		if err := c.Set("requirepass", "secret", true); err != nil {
			t.Fatal(err)
		}
	}}

	s, addr := testutil.StartServerOpts(t, opts)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "AUTH", "secret"),
		testutil.S("+OK", "SET", "k", "v"),
		testutil.S(":2", "RPUSH", "l", "a", "b"),
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "INCR", "n"),
		testutil.S("+QUEUED", "INCR", "n"),
		testutil.S("*[:1, :2]", "EXEC"),
	})
	if err := s.Shutdown(false); err != nil {
		t.Fatal(err)
	}

	_, addr = testutil.StartServerOpts(t, opts)
	c = testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "AUTH", "secret"),
		testutil.S(`"v"`, "GET", "k"),
		testutil.S(`*["a", "b"]`, "LRANGE", "l", "0", "-1"),
		testutil.S(`"2"`, "GET", "n"),
		testutil.S(":3", "DBSIZE"),
	})
}
