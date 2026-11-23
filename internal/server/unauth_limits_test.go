// Tests that clients which have not authenticated get Redis's small protocol
// limits when requirepass is set.
package server_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// rawExchange sends raw bytes and returns the first reply line and, when
// checkClose is set, whether the server then closed the connection.
func rawExchange(t *testing.T, addr, auth, payload string, checkClose bool) (string, bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	if auth != "" {
		fmt.Fprintf(conn, "*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(auth), auth)
		if line, _ := br.ReadString('\n'); line != "+OK\r\n" {
			t.Fatalf("got %q for AUTH, want +OK", line)
		}
	}
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	line, _ := br.ReadString('\n')
	if !checkClose {
		return strings.TrimRight(line, "\r\n"), false
	}
	// Closing with unread input makes the kernel send a reset, so any error
	// other than our deadline means the server closed the connection.
	_, err = br.ReadByte()
	var ne net.Error
	closed := err != nil && !(errors.As(err, &ne) && ne.Timeout())
	return strings.TrimRight(line, "\r\n"), closed
}

// command encodes args as a RESP multibulk.
func command(args ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return b.String()
}

func TestUnauthenticatedProtocolLimits(t *testing.T) {
	eleven := command("ECHO", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10")
	big := command("ECHO", strings.Repeat("x", 16*1024+1))
	cases := []struct {
		name      string
		pass      string
		auth      string
		payload   string
		want      string
		wantClose bool
	}{
		{"multibulk over 10", "secret", "", eleven, "-ERR Protocol error: unauthenticated multibulk length", true},
		{"bulk over 16 KiB", "secret", "", big, "-ERR Protocol error: unauthenticated bulk length", true},
		{"small command gets NOAUTH", "secret", "", command("PING"), "-NOAUTH Authentication required.", false},
		{"authenticated client unaffected", "secret", "secret", big, "$16385", false},
		{"no password unaffected", "", "", eleven, "-ERR wrong number of arguments for 'echo' command", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := testutil.StartServer(t, func(c *config.Config) {
				if err := c.Set("requirepass", tc.pass, true); err != nil {
					t.Fatal(err)
				}
			})
			got, closed := rawExchange(t, addr, tc.auth, tc.payload, tc.wantClose)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if closed != tc.wantClose {
				t.Fatalf("got closed=%v, want %v", closed, tc.wantClose)
			}
		})
	}
}
