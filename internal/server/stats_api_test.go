// Tests for the read-only Stats API.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

func TestServer_StatsSnapshot(t *testing.T) {
	srv, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	c.DoS("SET", "a", "1")
	first := srv.Stats()
	if want := srv.Config().Shards; len(first.Shards) != want {
		t.Errorf("shards: got %d, want %d", len(first.Shards), want)
	}
	if first.Role != "master" {
		t.Errorf("role: got %q, want master", first.Role)
	}
	if first.Clients < 1 {
		t.Errorf("clients: got %d, want >= 1", first.Clients)
	}
	c.DoS("GET", "a")
	second := srv.Stats()
	if second.Commands <= first.Commands {
		t.Errorf("commands: got %d then %d, want increase", first.Commands, second.Commands)
	}
	var keys int64
	for _, sh := range second.Shards {
		keys += sh.Keys
	}
	if keys != 1 {
		t.Errorf("keys: got %d, want 1", keys)
	}
}
