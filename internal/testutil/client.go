// Package testutil provides a minimal RESP client and helpers for starting
// DistCache (and optionally a real redis-server) in tests.
package testutil

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/server"
)

// Client is a synchronous RESP client.
type Client struct {
	Conn net.Conn
	r    *resp.Reader
	w    *bufio.Writer
}

// Dial connects to addr.
func Dial(addr string) (*Client, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	r := resp.NewReader(c, 64*1024)
	r.MaxBulkLen = 1 << 40
	return &Client{Conn: c, r: r, w: bufio.NewWriter(c)}, nil
}

// MustDial dials or fails the test.
func MustDial(t testing.TB, addr string) *Client {
	t.Helper()
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Close closes the connection.
func (c *Client) Close() error { return c.Conn.Close() }

// Send writes a command without reading the reply.
func (c *Client) Send(args ...string) error {
	_, err := c.w.Write(resp.AppendCommandStrings(nil, args...))
	if err != nil {
		return err
	}
	return c.w.Flush()
}

// Read reads one reply.
func (c *Client) Read() (resp.Value, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	return c.r.ReadValue()
}

// ReadSnapshot reads a full-sync snapshot transfer: "$<len>\r\n" followed
// by exactly len payload bytes. Unlike a RESP bulk string the payload has no
// trailing CRLF, so Read cannot be used for it. A non-'$' line (such as an
// error) is returned as an error. Blank keepalive lines, which Redis sends
// while it prepares the snapshot, are skipped.
func (c *Client) ReadSnapshot() ([]byte, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	line := ""
	for line == "" {
		var err error
		if line, err = c.r.ReadLine(); err != nil {
			return nil, err
		}
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(line, "$"), 10, 64)
	if !strings.HasPrefix(line, "$") || err != nil || n < 0 {
		return nil, fmt.Errorf("snapshot header: got %q, want $<len>", line)
	}
	defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }() // best-effort reset
	return c.r.ReadRaw(n)
}

// Do sends a command and reads its reply.
func (c *Client) Do(args ...string) (resp.Value, error) {
	if err := c.Send(args...); err != nil {
		return resp.Value{}, err
	}
	return c.Read()
}

// DoS sends a command and renders the reply as a string.
func (c *Client) DoS(args ...string) string {
	v, err := c.Do(args...)
	if err != nil {
		return "IOERR " + err.Error()
	}
	return Render(v, false)
}

// Render formats a reply canonically; sorted sorts aggregate elements
// (top level only) to compare unordered replies.
func Render(v resp.Value, sorted bool) string {
	switch v.Type {
	case '+':
		return "+" + string(v.Str)
	case '-':
		return "-" + string(v.Str)
	case ':':
		return ":" + strconv.FormatInt(v.Int, 10)
	case '$', '=':
		if v.Null {
			return "(nil)"
		}
		return strconv.Quote(string(v.Str))
	case '_':
		return "(nil)"
	case ',':
		return "," + string(v.Str)
	case '#':
		if v.Int == 1 {
			return "#t"
		}
		return "#f"
	case '*', '~', '>', '%':
		if v.Null {
			return "(nil-array)"
		}
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = Render(e, false)
		}
		if sorted {
			if v.Type == '%' {
				pairs := make([]string, 0, len(parts)/2)
				for i := 0; i+1 < len(parts); i += 2 {
					pairs = append(pairs, parts[i]+"=>"+parts[i+1])
				}
				sort.Strings(pairs)
				parts = pairs
			} else {
				sort.Strings(parts)
			}
		}
		return string(v.Type) + "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprintf("?%c", v.Type)
}

// Options configures StartServerOpts.
type Options struct {
	// Mutate, when non-nil, adjusts the config before the server is built.
	Mutate func(c *config.Config)
	// Clock, when non-nil, replaces the server's data-path clock before Start.
	Clock func() time.Time
}

// StartServer starts an in-process DistCache on a random port and returns
// its address. mutate can adjust the config.
func StartServer(t testing.TB, mutate func(c *config.Config)) (*server.Server, string) {
	t.Helper()
	return StartServerOpts(t, Options{Mutate: mutate})
}

// StartServerOpts starts an in-process DistCache on a random port configured
// by o and returns it with its address. The server is shut down on cleanup.
func StartServerOpts(t testing.TB, o Options) (*server.Server, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = []string{"127.0.0.1"}
	cfg.Port = 0
	cfg.Dir = t.TempDir()
	cfg.Save = nil
	if o.Mutate != nil {
		o.Mutate(cfg)
	}
	s := server.New(cfg, nil)
	if o.Clock != nil {
		s.SetClock(o.Clock)
	}
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
	return s, s.Addr()
}

// StartRedis returns the address of a real Redis to compare against; ok=false
// means none is available and callers should skip.
//
// If REDIS_SERVER_ADDR is set (CI provides a redis:8.4 service) that server is
// used: it is PINGed, skipped if its redis_version is below 8.4, and FLUSHALLed
// on cleanup. That server is shared, so tests using it must NOT run in
// parallel (no t.Parallel). Otherwise a private redis-server child process is
// started on a free port (unless DISTCACHE_SKIP_REDIS is set).
func StartRedis(t testing.TB) (addr string, ok bool) {
	t.Helper()
	if env := os.Getenv("REDIS_SERVER_ADDR"); env != "" {
		return useExternalRedis(t, env)
	}
	bin, err := exec.LookPath("redis-server")
	if err != nil || os.Getenv("DISTCACHE_SKIP_REDIS") != "" {
		return "", false
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", false
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // best-effort: cleanup after failure or teardown
	dir := t.TempDir()
	cmd := exec.Command(bin, "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--save", "",
		"--appendonly", "no", "--dir", dir, "--daemonize", "no", "--logfile", filepath.Join(dir, "redis.log"))
	if err := cmd.Start(); err != nil {
		return "", false
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	addr = "127.0.0.1:" + strconv.Itoa(port)
	for i := 0; i < 100; i++ {
		if c, err := Dial(addr); err == nil {
			if v, err := c.Do("PING"); err == nil && string(v.Str) == "PONG" {
				_ = c.Close() // best-effort: cleanup after failure or teardown
				return addr, true
			}
			_ = c.Close() // best-effort: cleanup after failure or teardown
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// RedisVersion returns the server version via INFO.
func RedisVersion(c *Client) string {
	v, err := c.Do("INFO", "server")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(v.Str), "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
		}
	}
	return ""
}

// useExternalRedis validates and adopts the Redis at addr.
func useExternalRedis(t testing.TB, addr string) (string, bool) {
	t.Helper()
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("REDIS_SERVER_ADDR=%s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if v, err := c.Do("PING"); err != nil || string(v.Str) != "PONG" {
		t.Fatalf("REDIS_SERVER_ADDR=%s: PING failed: %v %q", addr, err, v.Str)
	}
	if ver := RedisVersion(c); !versionAtLeast(ver, 8, 4) {
		t.Skipf("REDIS_SERVER_ADDR reports redis_version %q, need >= 8.4", ver)
	}
	t.Cleanup(func() {
		fc, err := Dial(addr)
		if err != nil {
			t.Errorf("FLUSHALL dial: %v", err)
			return
		}
		defer func() { _ = fc.Close() }()
		if _, err := fc.Do("FLUSHALL"); err != nil {
			t.Errorf("FLUSHALL: %v", err)
		}
	})
	if _, err := c.Do("FLUSHALL"); err != nil {
		t.Fatalf("FLUSHALL: %v", err)
	}
	return addr, true
}

// versionAtLeast reports whether a "major.minor[.patch]" string is >= major.minor.
func versionAtLeast(ver string, major, minor int) bool {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}
