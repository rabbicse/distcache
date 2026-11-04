// Integration tests for the persistence, lifecycle and replication-handshake
// commands that need a dedicated server: BGREWRITEAOF, CONFIG SET appendonly,
// SHUTDOWN, DEBUG, MODULE, SYNC/PSYNC and REPLCONF.
package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/internal/testutil"
)

func TestPersistence_BgRewriteAOFWithAppendOnly(t *testing.T) {
	var dir string
	_, addr := testutil.StartServer(t, func(c *config.Config) {
		c.AppendOnly.Store(true)
		dir = c.Dir
	})
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "SET", "k", "v"),
		testutil.S("+Background append only file rewriting started", "BGREWRITEAOF"),
	})
	waitInfoField(t, c, "persistence", "aof_rewrite_in_progress:0")
	if fi, err := os.Stat(filepath.Join(dir, "appendonly.aof")); err != nil || fi.Size() == 0 {
		t.Fatalf("appendonly.aof: got %v, %v, want a non-empty file", fi, err)
	}
}

func TestPersistence_ConfigSetAppendOnlyCreatesFile(t *testing.T) {
	var dir string
	_, addr := testutil.StartServer(t, func(c *config.Config) { dir = c.Dir })
	c := testutil.MustDial(t, addr)
	aof := filepath.Join(dir, "appendonly.aof")
	if _, err := os.Stat(aof); err == nil {
		t.Fatal("appendonly.aof exists before appendonly yes")
	}
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "SET", "k", "v"),
		testutil.S("+OK", "CONFIG", "SET", "appendonly", "yes"),
		testutil.S(`*["appendonly", "yes"]`, "CONFIG", "GET", "appendonly"),
	})
	waitInfoField(t, c, "persistence", "aof_rewrite_in_progress:0")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(aof); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not created after CONFIG SET appendonly yes", aof)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startDedicated starts a server the test controls, so SHUTDOWN can end it.
func startDedicated(t *testing.T) (*server.Server, string, <-chan error) {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = []string{"127.0.0.1"}
	cfg.Port = 0
	cfg.Dir = t.TempDir()
	cfg.Save = nil
	s := server.New(cfg, nil)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()
	select {
	case <-s.Ready():
	case err := <-errCh:
		t.Fatalf("server start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	t.Cleanup(func() { _ = s.Shutdown(false) })
	return s, s.Addr(), errCh
}

func TestPersistence_ShutdownClosesConnectionAndStopsServer(t *testing.T) {
	_, addr, errCh := startDedicated(t)
	c := testutil.MustDial(t, addr)
	testutil.RecordCommands(t)("shutdown")
	if err := c.Send("SHUTDOWN", "NOSAVE"); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Read(); err == nil {
		t.Fatalf("SHUTDOWN: got reply %s, want the connection to close", testutil.Render(v, false))
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after SHUTDOWN")
	}
}

func TestPersistence_ShutdownErrors(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("-ERR No shutdown in progress.", "SHUTDOWN", "ABORT"),
		testutil.S(errSyntax, "SHUTDOWN", "BOGUS"),
		testutil.S("+PONG", "PING"),
	})
}

func TestDebug_Commands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "DEBUG", "SLEEP", "0"),
		testutil.S("+OK", "SET", "s", "v"),
		testutil.S(":2", "RPUSH", "l", "a", "b"),
		testutil.S(":1", "SADD", "set", "m"),
		testutil.S(":1", "HSET", "h", "f", "v"),
		testutil.S(":1", "ZADD", "z", "1", "m"),
		testutil.S("+OK", "SET", "ttl", "v", "EX", "1000"),
	})
	digest := c.DoS("DEBUG", "DIGEST")
	testutil.RunScript(t, c, []testutil.Step{testutil.S("+OK", "DEBUG", "RELOAD")})
	if got := c.DoS("DEBUG", "DIGEST"); got != digest {
		t.Fatalf("DEBUG DIGEST after RELOAD: got %s want %s", got, digest)
	}
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`"v"`, "GET", "s"),
		testutil.S(`*["a", "b"]`, "LRANGE", "l", "0", "-1"),
		testutil.S(`*["f", "v"]`, "HGETALL", "h"),
		testutil.S(`*["m", "1"]`, "ZRANGE", "z", "0", "-1", "WITHSCORES"),
		testutil.S("+OK", "DEBUG", "SET-ACTIVE-EXPIRE", "0"),
		testutil.S("+OK", "DEBUG", "SET-ACTIVE-EXPIRE", "1"),
		testutil.S("+OK", "DEBUG", "LOG", "hello"),
		testutil.S("-ERR no such key", "DEBUG", "OBJECT", "nosuch"),
		testutil.S(errNotFloat, "DEBUG", "SLEEP", "x"),
	})
	if got := c.DoS("DEBUG", "OBJECT", "s"); !strings.HasPrefix(got, "+Value at:") {
		t.Fatalf("DEBUG OBJECT: got %s, want a +Value at: line", got)
	}
}

func TestModuleList(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("*[]", "MODULE", "LIST"),
	})
}

func TestReplicationHandshake_Replconf(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("+OK", "REPLCONF", "listening-port", "1"),
		testutil.S("+OK", "REPLCONF", "capa", "eof", "capa", "psync2"),
		testutil.S("-ERR Unrecognized REPLCONF option: bogus", "REPLCONF", "bogus", "1"),
		testutil.S(errSyntax, "REPLCONF", "listening-port"),
	})
}

func TestReplicationHandshake_PsyncFullResync(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	seed := testutil.MustDial(t, addr)
	testutil.RunScript(t, seed, []testutil.Step{testutil.S("+OK", "SET", "k", "v")})
	r := testutil.MustDial(t, addr)
	testutil.RunScript(t, r, []testutil.Step{testutil.S("+OK", "REPLCONF", "listening-port", "1")})
	testutil.RecordCommands(t)("psync")
	v, err := r.Do("PSYNC", "?", "-1")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(v.Str))
	if v.Type != '+' || len(fields) != 3 || fields[0] != "FULLRESYNC" || len(fields[1]) != 40 {
		t.Fatalf("PSYNC ? -1: got %s, want +FULLRESYNC <40-char replid> <offset>", testutil.Render(v, false))
	}
	// The snapshot payload follows as "$<len>\r\n<payload>" with no CRLF.
	payload, err := r.ReadSnapshot()
	if err != nil || len(payload) == 0 {
		t.Fatalf("snapshot payload: got %q, %v, want a non-empty snapshot", payload, err)
	}
}

func TestReplicationHandshake_Sync(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	r := testutil.MustDial(t, addr)
	testutil.RecordCommands(t)("sync")
	if err := r.Send("SYNC"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadSnapshot(); err != nil {
		t.Fatalf("SYNC: got %v, want a snapshot", err)
	}
}

// Redis sends +FULLRESYNC only to PSYNC clients; a legacy SYNC client gets the
// snapshot transfer directly.
func TestReplicationHandshake_SyncNoFullResyncLine(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	if err := c.Send("SYNC"); err != nil {
		t.Fatal(err)
	}
	payload, err := c.ReadSnapshot()
	if err != nil || len(payload) == 0 {
		t.Fatalf("SYNC: got %q, %v, want a non-empty snapshot", payload, err)
	}
}

func TestReplicationHandshake_PsyncBadOffset(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(errNotInt, "PSYNC", "?", "x"),
	})
}
