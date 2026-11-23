// Tests that protected configs (dir, dbfilename) refuse CONFIG SET unless
// enable-protected-configs allows it, and that dbfilename must be a filename.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

func TestConfigSet_ProtectedConfigs(t *testing.T) {
	cases := []struct {
		name   string
		enable string
		args   []string
		want   string
	}{
		{"dir refused by default", "no", []string{"CONFIG", "SET", "Dir", "/tmp"},
			"-ERR CONFIG SET failed (possibly related to argument 'Dir') - can't set protected config"},
		{"dbfilename refused by default", "no", []string{"CONFIG", "SET", "dbfilename", "x.sh"},
			"-ERR CONFIG SET failed (possibly related to argument 'dbfilename') - can't set protected config"},
		{"maxmemory unprotected", "no", []string{"CONFIG", "SET", "maxmemory", "10mb"}, "+OK"},
		{"dbfilename allowed when enabled", "yes", []string{"CONFIG", "SET", "dbfilename", "x.dcs"}, "+OK"},
		{"local allows loopback", "local", []string{"CONFIG", "SET", "dbfilename", "x.dcs"}, "+OK"},
		{"dbfilename path rejected", "yes", []string{"CONFIG", "SET", "dbfilename", "../y.sh"},
			"-ERR CONFIG SET failed (possibly related to argument 'dbfilename') - dbfilename can't be a path, just a filename"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := testutil.StartServer(t, func(c *config.Config) {
				if err := c.Set("enable-protected-configs", tc.enable, true); err != nil {
					t.Fatal(err)
				}
			})
			c := testutil.MustDial(t, addr)
			if got := c.DoS(tc.args...); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestConfigSetFromClient_LocalRefusesRemote(t *testing.T) {
	c := config.Default()
	if err := c.Set("enable-protected-configs", "local", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFromClient("dir", "/tmp", false); err == nil {
		t.Fatalf("got nil error for a remote client, want can't set protected config")
	}
	if err := c.SetFromClient("dir", "/tmp", true); err != nil {
		t.Fatalf("got %v for a local client, want nil", err)
	}
}

func TestConfigSet_EnableProtectedConfigsImmutable(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	want := "-ERR CONFIG SET failed (possibly related to argument 'enable-protected-configs') - can't set immutable config"
	if got := c.DoS("CONFIG", "SET", "enable-protected-configs", "yes"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
