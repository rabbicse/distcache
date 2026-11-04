// clock.go provides a controllable clock for tests that
// need deterministic expiry, LRU/LFU and stream-ID behaviour.
package testutil

import (
	"sync"
	"time"
)

// FakeClock is a manually driven clock safe for concurrent use. Pass its Now
// method as Options.Clock. It does not drive timers or tickers.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time // guarded by mu
}

// NewFakeClock returns a FakeClock that reads start until moved.
func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

// Now returns the clock's current time.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d (backward if d is negative).
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set moves the clock to t, which may be before the current time.
func (f *FakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}
