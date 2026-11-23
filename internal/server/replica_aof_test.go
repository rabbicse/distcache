// Tests that a replica running with appendonly yes keeps the dataset it got
// in a full sync, plus the stream that followed, in its AOF across restarts.
package server_test

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// waitDBSize polls DBSIZE until it is want, failing after 10s.
func waitDBSize(t *testing.T, c *testutil.Client, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := c.DoS("DBSIZE")
		if got == ":"+strconv.Itoa(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("DBSIZE: got %s, want :%d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReplication_ReplicaAOFKeepsFullSyncData(t *testing.T) {
	const n = 100
	_, maddr := testutil.StartServer(t, nil)
	m := testutil.MustDial(t, maddr)
	for i := range n {
		m.DoS("SET", "k"+strconv.Itoa(i), strconv.Itoa(i))
	}

	dir := t.TempDir()
	rs, r := startAOFServer(t, dir)
	r.DoS("SET", "stale", "pre-sync")
	host, port, _ := net.SplitHostPort(maddr)
	if got := r.DoS("REPLICAOF", host, port); got != "+OK" {
		t.Fatalf("REPLICAOF: got %s, want +OK", got)
	}
	waitDBSize(t, r, n)
	m.DoS("SET", "post", "1")
	waitDBSize(t, r, n+1)
	m.DoS("DEL", "k0")
	waitDBSize(t, r, n)
	if got := r.DoS("GET", "post"); got != `"1"` {
		t.Fatalf("GET post on replica: got %s, want \"1\"", got)
	}
	if err := rs.Shutdown(false); err != nil {
		t.Fatal(err)
	}

	_, r = startAOFServer(t, dir)
	if got, want := r.DoS("DBSIZE"), ":"+strconv.Itoa(n); got != want {
		t.Errorf("DBSIZE after replica restart: got %s, want %s", got, want)
	}
	for k, want := range map[string]string{"k1": `"1"`, "k99": `"99"`, "post": `"1"`, "k0": "(nil)", "stale": "(nil)"} {
		if got := r.DoS("GET", k); got != want {
			t.Errorf("GET %s after replica restart: got %s, want %s", k, got, want)
		}
	}
}
