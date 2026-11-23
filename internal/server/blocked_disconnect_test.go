// Regression tests: a client that disconnects while blocked is unblocked and
// never pops data, even when it had pipelined more input behind the command.
package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// waitBlockedClients polls INFO clients until blocked_clients is n.
func waitBlockedClients(t *testing.T, c *testutil.Client, n string) {
	t.Helper()
	want := "blocked_clients:" + n + `\r\n` // DoS escapes CRLF
	deadline := time.Now().Add(5 * time.Second)
	for {
		info := c.DoS("INFO", "clients")
		if strings.Contains(info, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %s, want %s", info, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBlocking_DisconnectWithPipelinedInput closes a client blocked in BLPOP
// that sent more commands after it, then checks the waiter is gone and a
// later LPUSH keeps its element.
func TestBlocking_DisconnectWithPipelinedInput(t *testing.T) {
	tests := []struct {
		name string
		// send writes the blocking command plus trailing input to c.
		send func(t *testing.T, c, admin *testutil.Client)
	}{
		{"pipelined in one write", func(t *testing.T, c, admin *testutil.Client) {
			if _, err := c.Conn.Write([]byte("*3\r\n$5\r\nBLPOP\r\n$1\r\nq\r\n$1\r\n0\r\n*1\r\n$4\r\nPING\r\n")); err != nil {
				t.Fatal(err)
			}
			waitBlockedClients(t, admin, "1")
		}},
		{"sent while blocked", func(t *testing.T, c, admin *testutil.Client) {
			if _, err := c.Conn.Write([]byte("*3\r\n$5\r\nBLPOP\r\n$1\r\nq\r\n$1\r\n0\r\n")); err != nil {
				t.Fatal(err)
			}
			waitBlockedClients(t, admin, "1")
			if _, err := c.Conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond) // let the server read the PING
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := testutil.StartServer(t, nil)
			admin := testutil.MustDial(t, addr)
			c := testutil.MustDial(t, addr)
			tc.send(t, c, admin)
			_ = c.Close()
			waitBlockedClients(t, admin, "0")
			if got := admin.DoS("LPUSH", "q", "x"); got != ":1" {
				t.Fatalf("LPUSH: got %s, want :1", got)
			}
			if got := admin.DoS("LLEN", "q"); got != ":1" {
				t.Fatalf("LLEN: got %s, want :1", got)
			}
		})
	}
}
