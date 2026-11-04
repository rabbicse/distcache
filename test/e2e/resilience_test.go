//go:build !windows

// Resilience tests: crash recovery of a SIGKILLed server subprocess and
// survival of a panicking command handler.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// serverBinary builds cmd/distcache-server once per test run.
func serverBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "distcache-e2e-bin")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "distcache-server")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/distcache-server")
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

// proc is a server subprocess and a client connected to it.
type proc struct {
	cmd *exec.Cmd
	log *bytes.Buffer
	rc  *redis.Client
}

// startProc launches the server binary on a free port with dir and args and
// waits up to 10 s for it to answer PING. It is killed on cleanup.
func startProc(t *testing.T, dir string, args ...string) *proc {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // free the port for the server; a tiny reuse race is acceptable in tests
	all := append([]string{"--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--dir", dir, "--save", ""}, args...)
	p := &proc{cmd: exec.Command(serverBinary(t), all...), log: &bytes.Buffer{}}
	p.cmd.Stdout, p.cmd.Stderr = p.log, p.log
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.rc = redis.NewClient(&redis.Options{Addr: "127.0.0.1:" + strconv.Itoa(port), PoolSize: 16, MaxRetries: -1})
	t.Cleanup(func() {
		_ = p.rc.Close()         // best-effort: teardown
		_ = p.cmd.Process.Kill() // no-op if already dead
		_, _ = p.cmd.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for p.rc.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatalf("server did not answer PING within 10s; log:\n%s", p.log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return p
}

// quietLogger drops go-redis's internal log lines; the crash test makes it
// log every failed redial after the SIGKILL, which is expected noise.
type quietLogger struct{}

func (quietLogger) Printf(context.Context, string, ...interface{}) {}

func TestMain(m *testing.M) {
	redis.SetLogger(quietLogger{})
	os.Exit(m.Run())
}

// ack is one acknowledged write by a worker.
type ack struct {
	seq int
	at  time.Time
}

func TestCrashRecoveryEverysec(t *testing.T) {
	tests := []struct {
		fsync string
		// grace is how long before the kill a write must have been acked to
		// be required after restart; 0 means every acked write.
		grace time.Duration
	}{
		{fsync: "everysec", grace: 1500 * time.Millisecond},
		{fsync: "always", grace: 0},
	}
	for _, tc := range tests {
		t.Run(tc.fsync, func(t *testing.T) {
			dir := t.TempDir()
			args := []string{"--appendonly", "yes", "--appendfsync", tc.fsync}
			p := startProc(t, dir, args...)

			const workers = 8
			acks := make([][]ack, workers)
			stop := make(chan struct{})
			var wg sync.WaitGroup
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ctx := context.Background()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
						}
						if p.rc.Set(ctx, fmt.Sprintf("k:%d:%d", w, i), i, 0).Err() != nil {
							return // server killed
						}
						if p.rc.Incr(ctx, fmt.Sprint("ctr:", w)).Err() != nil {
							return
						}
						acks[w] = append(acks[w], ack{seq: i, at: time.Now()})
					}
				}()
			}
			time.Sleep(3 * time.Second) // the write window the spec prescribes
			killAt := time.Now()
			if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			_, _ = p.cmd.Process.Wait() // reap; exit status is "killed"
			close(stop)
			wg.Wait()

			p2 := startProc(t, dir, args...)
			ctx := context.Background()
			total := 0
			for w := range workers {
				required := 0
				for _, a := range acks[w] {
					if tc.grace == 0 || a.at.Before(killAt.Add(-tc.grace)) {
						required = a.seq + 1
					}
				}
				total += required
				if required == 0 {
					continue
				}
				last := fmt.Sprintf("k:%d:%d", w, required-1)
				if got := p2.rc.Get(ctx, last).Val(); got != strconv.Itoa(required-1) {
					t.Errorf("worker %d: %s = %q after restart, want %d", w, last, got, required-1)
				}
				if n, _ := p2.rc.Get(ctx, fmt.Sprint("ctr:", w)).Int(); n < required {
					t.Errorf("worker %d: ctr = %d after restart, want >= %d", w, n, required)
				}
				for i := 0; i < required; i += 97 { // sample earlier writes too
					if got := p2.rc.Get(ctx, fmt.Sprintf("k:%d:%d", w, i)).Val(); got != strconv.Itoa(i) {
						t.Fatalf("worker %d: k:%d:%d = %q, want %d", w, w, i, got, i)
					}
				}
			}
			if total == 0 {
				t.Fatal("no writes were required to survive; the workload did not run")
			}
			t.Logf("%s: %d acknowledged writes required and present", tc.fsync, total)
		})
	}
}

func TestPanicDoesNotKillServer(t *testing.T) {
	ctx := context.Background()
	s, rc := startNode(t, t.TempDir(), nil)
	err := rc.Do(ctx, "DEBUG", "PANIC").Err()
	if err == nil {
		t.Fatal("DEBUG PANIC: got nil error, want an error reply")
	}
	if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("DEBUG PANIC: connection dropped (%v), want an error reply", err)
	}
	fresh := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = fresh.Close() }) // best-effort: teardown
	if v, err := fresh.Ping(ctx).Result(); err != nil || v != "PONG" {
		t.Fatalf("PING after panic: got %q, %v; want PONG", v, err)
	}
}
