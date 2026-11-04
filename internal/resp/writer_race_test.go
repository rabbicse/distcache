package resp

import (
	"sync"
	"sync/atomic"
	"testing"
)

// lockCheckWriter fails the test when Write is called without the guarding
// mutex held, which is how an interleaving socket write would look.
type lockCheckWriter struct {
	mu       *sync.Mutex
	unlocked atomic.Int64
}

func (r *lockCheckWriter) Write(p []byte) (int, error) {
	if r.mu.TryLock() {
		r.mu.Unlock()
		r.unlocked.Add(1)
	}
	return len(p), nil
}

func TestWriter_Flush_HoldsMutex(t *testing.T) {
	var mu sync.Mutex
	out := &lockCheckWriter{mu: &mu}
	w := NewWriter(out, 2)
	w.Mu = &mu

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			w.WriteBulk(make([]byte, 70*1024)) // crosses flushThreshold
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			mu.Lock()
			_, _ = out.Write([]byte("PUSH"))
			mu.Unlock()
		}
	}()
	wg.Wait()

	if n := out.unlocked.Load(); n != 0 {
		t.Fatalf("got %d writes without the mutex held, want 0", n)
	}
	if w.Flushes() == 0 {
		t.Fatal("got 0 flushes, want > 0")
	}
}
