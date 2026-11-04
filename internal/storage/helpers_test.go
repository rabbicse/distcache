// Shared helpers for the storage unit tests.
package storage

import (
	"testing"
	"time"
)

const baseMs = int64(1_700_000_000_000)

// newKS returns a small keyspace whose clock is pinned to baseMs.
func newKS(t *testing.T) *Keyspace {
	t.Helper()
	ks := New(8, 2)
	ks.SetClock(func() time.Time { return time.UnixMilli(baseMs) })
	return ks
}

// writeDB returns a write view on db 0 at the given time.
func writeDB(ks *Keyspace, now int64) *DB {
	return &DB{KS: ks, Idx: 0, Now: now, Write: true}
}

// withLock runs fn holding the write locks of keys' shards.
func withLock(ks *Keyspace, keys []string, fn func()) {
	set := ks.ShardSet(keys, nil)
	ks.Lock(set, true)
	defer ks.Unlock(set, true)
	fn()
}
