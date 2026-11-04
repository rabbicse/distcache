package storage

import (
	"bufio"
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// collectSnapshot reads the frozen view of every shard concurrently.
func collectSnapshot(ks *Keyspace) map[string]string {
	var mu sync.Mutex
	got := map[string]string{}
	var wg sync.WaitGroup
	for i := range ks.NumShards() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ks.SnapshotShard(i, func(db int, key string, e *Entry) {
				mu.Lock()
				got[fmt.Sprintf("%d/%s", db, key)] = string(e.Value.([]byte))
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	return got
}

// snapshotFixture seeds 200 old keys and begins a snapshot.
func snapshotFixture(t *testing.T) (ks *Keyspace, orig map[string]string, ops []func()) {
	t.Helper()
	ks = newKS(t)
	const nOld = 200
	orig = map[string]string{}
	w := writeDB(ks, baseMs)
	ks.LockAll(true)
	for i := range nOld {
		k := fmt.Sprintf("old%d", i)
		v := fmt.Sprintf("orig%d", i)
		w.Add(k, []byte(v))
		orig["0/"+k] = v
	}
	ks.UnlockAll(true)

	var cutKeys int
	ks.BeginSnapshot(func() {
		// cut runs with all shards write-locked: a consistent instant.
		for i := range ks.NumShards() {
			cutKeys += ks.ShardLen(i, 0)
		}
	})
	if !ks.SnapshotActive() {
		t.Fatal("SnapshotActive: got false after Begin")
	}
	if cutKeys != nOld {
		t.Fatalf("cut saw %d keys, want %d", cutKeys, nOld)
	}

	// mutate follows the server's writer protocol: lock, Preserve, mutate.
	mutate := func(key string, fn func(db *DB)) func() {
		return func() {
			withLock(ks, []string{key}, func() {
				db := writeDB(ks, baseMs)
				db.Preserve(key)
				fn(db)
			})
		}
	}
	// Interleave 100 inserts, 10 overwrites and 10 deletes.
	for i := range 100 {
		k := fmt.Sprintf("new%d", i)
		ops = append(ops, mutate(k, func(db *DB) { db.Add(k, []byte("fresh")) }))
		if i < 10 {
			ko := fmt.Sprintf("old%d", i)
			kd := fmt.Sprintf("old%d", 10+i)
			ops = append(ops,
				mutate(ko, func(db *DB) { db.Add(ko, []byte("overwritten")) }),
				mutate(kd, func(db *DB) { db.Delete(kd) }))
		}
	}
	return ks, orig, ops
}

func assertFrozenView(t *testing.T, snap, orig map[string]string) {
	t.Helper()
	if len(snap) != len(orig) {
		t.Errorf("snapshot has %d keys, want %d", len(snap), len(orig))
	}
	for k, want := range orig {
		if got, ok := snap[k]; !ok || got != want {
			t.Errorf("snapshot[%s]: got %q (present=%v), want %q", k, got, ok, want)
		}
	}
	for k := range snap {
		if _, ok := orig[k]; !ok {
			t.Errorf("snapshot contains post-cut key %s", k)
		}
	}
}

func assertLiveAfterWrites(t *testing.T, ks *Keyspace) {
	t.Helper()
	live := writeDB(ks, baseMs)
	ks.LockAll(false)
	defer ks.UnlockAll(false)
	for i := range 100 {
		if !live.Exists(fmt.Sprintf("new%d", i)) {
			t.Errorf("live key new%d missing", i)
		}
	}
	if e := live.Peek("old3"); e == nil || string(e.Value.([]byte)) != "overwritten" {
		t.Errorf("live old3: got %v, want overwritten", e)
	}
	if live.Peek("old15") != nil {
		t.Error("live old15 should be deleted")
	}
	if got, want := ks.DBSize(0), int64(200+100-10); got != want {
		t.Errorf("DBSize: got %d, want %d", got, want)
	}
}

// TestSnapshot_IsPointInTime_WritesBeforeRead completes every write before
// the snapshot is read, so the result depends only on Preserve being correct.
func TestSnapshot_IsPointInTime_WritesBeforeRead(t *testing.T) {
	ks, orig, ops := snapshotFixture(t)
	for _, op := range ops {
		op()
	}
	snap := collectSnapshot(ks)
	ks.EndSnapshot()
	if ks.SnapshotActive() {
		t.Fatal("SnapshotActive: got true after End")
	}
	assertFrozenView(t, snap, orig)
	assertLiveAfterWrites(t, ks)
}

// TestSnapshot_IsPointInTime_ConcurrentWrites starts the reader only after the
// first half of the writes finished, while the second half races with it.
func TestSnapshot_IsPointInTime_ConcurrentWrites(t *testing.T) {
	ks, orig, ops := snapshotFixture(t)
	half := len(ops) / 2
	var firstDone, rest sync.WaitGroup
	release := make(chan struct{})
	for i, op := range ops {
		if i < half {
			firstDone.Add(1)
			go func() { defer firstDone.Done(); op() }()
			continue
		}
		rest.Add(1)
		go func() { defer rest.Done(); <-release; op() }()
	}
	firstDone.Wait() // first N writes complete before the reader starts
	var snap map[string]string
	rest.Add(1)
	go func() {
		defer rest.Done()
		snap = collectSnapshot(ks)
	}()
	close(release) // remaining writers run through the read
	rest.Wait()
	ks.EndSnapshot()
	assertFrozenView(t, snap, orig)
	assertLiveAfterWrites(t, ks)
}

func TestSnapshot_FlushAndExpirePreserved(t *testing.T) {
	ks := New(4, 1)
	w := writeDB(ks, baseMs)
	ks.LockAll(true)
	for i := range 20 {
		k := fmt.Sprintf("k%d", i)
		e := w.Add(k, []byte("v"))
		if i < 5 {
			w.SetExpire(k, e, baseMs+1)
		}
	}
	ks.UnlockAll(true)

	ks.BeginSnapshot(nil)
	ks.LockAll(true)
	for i := range ks.NumShards() {
		ks.ExpireShard(i, 0, 100, baseMs+10, nil) // reclaims the 5 volatile keys
	}
	ks.FlushDB(0)
	ks.UnlockAll(true)
	snap := collectSnapshot(ks)
	ks.EndSnapshot()
	if len(snap) != 20 {
		t.Fatalf("snapshot after expire+flush: got %d keys, want 20", len(snap))
	}
	if ks.DBSize(0) != 0 {
		t.Fatalf("live DBSize: got %d, want 0", ks.DBSize(0))
	}
}

func TestSnapshot_EncodesFrozenView(t *testing.T) {
	ks := newKS(t)
	w := writeDB(ks, baseMs)
	ks.LockAll(true)
	for i := range 30 {
		w.Add(fmt.Sprintf("k%d", i), []byte("v"))
	}
	ks.UnlockAll(true)
	ks.BeginSnapshot(nil)
	withLock(ks, []string{"k1"}, func() {
		w.Preserve("k1")
		w.Delete("k1")
	})
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for i := range ks.NumShards() {
		ks.SnapshotShard(i, func(db int, key string, e *Entry) { enc.WriteEntry(db, key, e) })
	}
	ks.EndSnapshot()
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := Decode(bufio.NewReader(&buf), func(int, string, any, int64) error { n++; return nil })
	if err != nil || n != 30 {
		t.Fatalf("decode: n=%d err=%v, want 30/nil", n, err)
	}
}

func TestSnapshot_PreserveWithoutSnapshotIsNoop(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"k"}, func() {
		db.Add("k", []byte("v"))
		db.Preserve("k")
		e := db.Peek("k")
		if e == nil || string(e.Value.([]byte)) != "v" {
			t.Fatalf("entry changed by Preserve: %v", e)
		}
	})
	for i, sh := range ks.shards {
		if len(sh.overlay) != 0 || sh.pending {
			t.Errorf("shard %d: overlay=%v pending=%v, want empty/false", i, sh.overlay, sh.pending)
		}
	}
}
