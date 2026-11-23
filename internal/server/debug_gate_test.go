// Tests for the DEBUG gate: the command is off unless enable-debug-command
// allows it, and it can never be queued in a transaction.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

const errDebugNotAllowed = `-ERR DEBUG command not allowed. If the enable-debug-command option is set to "local", ` +
	`you can run it from a local connection, otherwise you need to set this option in the configuration file, ` +
	`and then restart the server.`

func TestDebug_GatedByEnableDebugCommand(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"no", errDebugNotAllowed},
		{"local", "+OK"}, // the test client connects over loopback
		{"yes", "+OK"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, addr := testutil.StartServer(t, func(c *config.Config) {
				if err := c.Set("enable-debug-command", tc.mode, true); err != nil {
					t.Fatal(err)
				}
			})
			c := testutil.MustDial(t, addr)
			if got := c.DoS("DEBUG", "SLEEP", "0"); got != tc.want {
				t.Errorf("DEBUG SLEEP 0 = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestDebug_DefaultIsOff checks the shipped default, which the test helper
// overrides for every other test.
func TestDebug_DefaultIsOff(t *testing.T) {
	if got := config.Default().EnableDebugCommand(); got != "no" {
		t.Fatalf("default enable-debug-command = %q, want no", got)
	}
}

func TestDebug_CannotChangeAtRuntime(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	want := "-ERR CONFIG SET failed (possibly related to argument 'enable-debug-command') - can't set immutable config"
	if got := c.DoS("CONFIG", "SET", "enable-debug-command", "yes"); got != want {
		t.Errorf("CONFIG SET enable-debug-command = %s, want %s", got, want)
	}
}

// TestDebug_RefusedInsideMulti: DEBUG DIGEST and DEBUG RELOAD take every
// shard lock themselves, so running them from EXEC, which already holds
// locks, used to deadlock the whole server.
func TestDebug_RefusedInsideMulti(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "SET", "k", "v"),
		testutil.S("-ERR Command not allowed inside a transaction", "DEBUG", "DIGEST"),
		testutil.S("-EXECABORT Transaction discarded because of previous errors.", "EXEC"),
		testutil.S("+PONG", "PING"),
		testutil.S("(nil)", "GET", "k"),
	})
}
