// Regression tests: every write acknowledged before shutdown is persisted in
// the snapshot or the AOF, and shutdown does not hang on blocked clients or
// long scripts.
package server_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// TestShutdown_AcknowledgedWritesPersist runs INCR writers while the server
// shuts down, restarts it on the same directory and checks that each counter
// is at least its last acknowledged value.
func TestShutdown_AcknowledgedWritesPersist(t *testing.T) {
	tests := []struct {
		name string
		aof  bool
	}{
		{"snapshot", false},
		{"aof", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mutate := func(c *config.Config) {
				c.Dir = dir
				c.AppendOnly.Store(tc.aof)
			}
			s, addr := testutil.StartServer(t, mutate)
			admin := testutil.MustDial(t, addr)
			// A larger dataset makes the final save slow, widening the window
			// in which unpersisted writes could be acknowledged.
			if got := admin.DoS("EVAL", "for i=1,100000 do redis.call('SET','fill:'..i,'xxxxxxxxxxxxxxxx') end return 1", "0"); got != ":1" {
				t.Fatalf("fill: got %s, want :1", got)
			}
			const writers = 4
			acked := make([]int64, writers)
			var wg sync.WaitGroup
			started := make(chan struct{}, writers)
			for i := range writers {
				c := testutil.MustDial(t, addr)
				wg.Add(1)
				go func() {
					defer wg.Done()
					key := "ctr:" + strconv.Itoa(i)
					for n := 0; ; n++ {
						v, err := c.Do("INCR", key)
						if err != nil || v.Type != ':' {
							return
						}
						acked[i] = v.Int
						if n == 100 {
							started <- struct{}{}
						}
					}
				}()
			}
			for range writers {
				<-started
			}
			if err := s.Shutdown(!tc.aof); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			wg.Wait()

			_, addr2 := testutil.StartServer(t, mutate)
			c := testutil.MustDial(t, addr2)
			for i := range writers {
				got := c.DoS("GET", "ctr:"+strconv.Itoa(i))
				n, _ := strconv.ParseInt(strings.Trim(got, `"`), 10, 64)
				if n < acked[i] {
					t.Errorf("ctr:%d: got %s after restart, want >= %d (acknowledged)", i, got, acked[i])
				}
			}
		})
	}
}

// TestShutdown_DoesNotHang shuts down while one client is blocked in BLPOP
// and another runs an endless script.
func TestShutdown_DoesNotHang(t *testing.T) {
	s, addr := testutil.StartServer(t, func(c *config.Config) { c.AppendOnly.Store(true) })
	admin := testutil.MustDial(t, addr)
	blocked := testutil.MustDial(t, addr)
	if err := blocked.Send("BLPOP", "q", "0"); err != nil {
		t.Fatal(err)
	}
	script := testutil.MustDial(t, addr)
	if err := script.Send("EVAL", "redis.call('SET','x','1') while true do end", "0"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(admin.DoS("INFO", "clients"), `blocked_clients:1\r\n`) {
		if time.Now().After(deadline) {
			t.Fatal("client did not block")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the script start
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown hung")
	}
}
