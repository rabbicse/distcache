package storage

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/ds"
)

func TestShardSet_SortedAndDeduped(t *testing.T) {
	ks := New(8, 1)
	var keys []string
	for i := range 200 {
		keys = append(keys, fmt.Sprintf("key-%d", i))
	}
	got := ks.ShardSet(keys, nil)
	if !sort.IntsAreSorted(got) {
		t.Fatalf("not ascending: %v", got)
	}
	seen := map[int]bool{}
	for _, s := range got {
		if seen[s] {
			t.Fatalf("duplicate shard %d in %v", s, got)
		}
		seen[s] = true
	}
	if len(got) != 8 {
		t.Fatalf("got %d shards, want 8 (200 keys should cover all)", len(got))
	}
	// Same key repeated collapses to one shard.
	if got := ks.ShardSet([]string{"a", "a", "a"}, nil); len(got) != 1 || got[0] != ks.ShardOf("a") {
		t.Fatalf("got %v, want [%d]", got, ks.ShardOf("a"))
	}
	if got := ks.ShardSet(nil, nil); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestNew_InvalidShardCountFallsBack(t *testing.T) {
	for _, n := range []int{0, 3, -1} {
		if got := New(n, 1).NumShards(); got != 256 {
			t.Errorf("New(%d): got %d shards, want 256", n, got)
		}
	}
}

func TestLockOrdering_NoDeadlock(t *testing.T) {
	ks := New(8, 1)
	var keys []string
	for i := range 64 {
		keys = append(keys, fmt.Sprintf("k%d", i))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(g), 1))
				var buf []int
				for range 1000 {
					set := []string{keys[r.IntN(64)], keys[r.IntN(64)], keys[r.IntN(64)]}
					write := r.IntN(2) == 0
					buf = ks.ShardSet(set, buf)
					ks.Lock(buf, write)
					ks.Unlock(buf, write)
				}
			}()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lock ordering test did not finish within 10s (deadlock?)")
	}
}

func TestLockAllAndSingleShard(t *testing.T) {
	ks := New(4, 1)
	for _, write := range []bool{true, false} {
		ks.LockAll(write)
		ks.UnlockAll(write)
		ks.LockShard(2, write)
		ks.UnlockShard(2, write)
	}
}

func TestDB_SetGetDeleteAccounting(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"k"}, func() {
		db.Add("k", []byte("value"))
		if got := ks.DBSize(0); got != 1 {
			t.Fatalf("DBSize: got %d, want 1", got)
		}
		if ks.UsedMemory() <= 0 {
			t.Fatalf("UsedMemory: got %d, want > 0", ks.UsedMemory())
		}
		if !db.Exists("k") || db.Peek("k") == nil {
			t.Fatal("key should exist")
		}
		if !db.Delete("k") {
			t.Fatal("Delete: got false, want true")
		}
		if db.Delete("k") {
			t.Fatal("second Delete: got true, want false")
		}
		if ks.DBSize(0) != 0 || ks.UsedMemory() != 0 {
			t.Fatalf("after delete: size=%d mem=%d, want 0/0", ks.DBSize(0), ks.UsedMemory())
		}
	})
}

func TestDB_SetOverwriteAdjustsMemoryAndTTL(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"k"}, func() {
		e := db.Add("k", []byte("a"))
		db.SetExpire("k", e, baseMs+1000)
		if ks.ExpiresCount(0) != 1 {
			t.Fatalf("ExpiresCount: got %d, want 1", ks.ExpiresCount(0))
		}
		small := ks.UsedMemory()
		db.Add("k", make([]byte, 1000)) // replaces, drops TTL
		if ks.DBSize(0) != 1 || ks.ExpiresCount(0) != 0 {
			t.Fatalf("size=%d expires=%d, want 1/0", ks.DBSize(0), ks.ExpiresCount(0))
		}
		if ks.UsedMemory() <= small {
			t.Fatalf("memory did not grow: %d <= %d", ks.UsedMemory(), small)
		}
		e = db.Peek("k")
		db.SetExpire("k", e, baseMs+5)
		db.SetExpire("k", e, 0) // persist
		if ks.ExpiresCount(0) != 0 {
			t.Fatalf("ExpiresCount after persist: got %d, want 0", ks.ExpiresCount(0))
		}
		db.Delete("k")
		if ks.UsedMemory() != 0 {
			t.Fatalf("memory leak: %d", ks.UsedMemory())
		}
	})
}

func TestDB_UpdatedRecomputesMemory(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"l"}, func() {
		l := ds.NewList()
		db.Add("l", l)
		before := ks.UsedMemory()
		for range 100 {
			l.PushBack(make([]byte, 100))
		}
		db.Updated("l")
		if ks.UsedMemory() <= before {
			t.Fatalf("memory: got %d, want > %d", ks.UsedMemory(), before)
		}
		db.Updated("absent") // no-op
		db.Delete("l")
		if ks.UsedMemory() != 0 {
			t.Fatalf("memory after delete: got %d, want 0", ks.UsedMemory())
		}
	})
}

func TestDB_SetValueKeepTTL(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"k"}, func() {
		e := db.Add("k", []byte("a"))
		db.SetExpire("k", e, baseMs+500)
		if got := db.SetValue("k", []byte("b"), true).ExpireAt; got != baseMs+500 {
			t.Fatalf("keepTTL: got %d, want %d", got, baseMs+500)
		}
		if got := db.SetValue("k", []byte("c"), false).ExpireAt; got != 0 {
			t.Fatalf("drop TTL: got %d, want 0", got)
		}
	})
}

func TestTypeNameValueBytesClone(t *testing.T) {
	zs := ds.NewZSet()
	zs.Add("m", 1)
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"string", []byte("x"), TypeString},
		{"list", ds.NewList(), TypeList},
		{"set", ds.NewSet(), TypeSet},
		{"zset", zs, TypeZSet},
		{"hash", ds.NewHash(), TypeHash},
		{"stream", ds.NewStream(), TypeStream},
		{"unknown", 42, TypeNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TypeName(tc.v); got != tc.want {
				t.Errorf("TypeName: got %q, want %q", got, tc.want)
			}
			_ = ValueBytes(tc.v)
			c := CloneValue(tc.v)
			if TypeName(c) != tc.want {
				t.Errorf("clone type: got %q, want %q", TypeName(c), tc.want)
			}
		})
	}
	b := []byte("abc")
	c := CloneValue(b).([]byte)
	c[0] = 'z'
	if string(b) != "abc" {
		t.Error("CloneValue aliased the byte slice")
	}
}

func TestIgnoreExpiryReplicaMode(t *testing.T) {
	ks := newKS(t)
	var calls int
	hook := func(int, string) { calls++ }
	withLock(ks, []string{"k"}, func() {
		w := writeDB(ks, baseMs)
		e := w.Add("k", []byte("v"))
		w.SetExpire("k", e, baseMs+100)

		ign := &DB{KS: ks, Now: baseMs + 101, Write: true, IgnoreExpiry: true, OnExpire: hook}
		if ign.Lookup("k") == nil {
			t.Fatal("IgnoreExpiry: key not found")
		}
		rep := &DB{KS: ks, Now: baseMs + 101, Write: true, ReplicaMode: true, OnExpire: hook}
		if rep.Lookup("k") != nil {
			t.Fatal("replica: expired key should read as missing")
		}
		if ks.DBSize(0) != 1 || calls != 0 {
			t.Fatalf("replica must not reclaim: size=%d hook calls=%d", ks.DBSize(0), calls)
		}
		ro := &DB{KS: ks, Now: baseMs + 101, Write: false, OnExpire: hook}
		if ro.Lookup("k") != nil || ro.Exists("k") || ks.DBSize(0) != 1 {
			t.Fatal("read-only view must hide but not reclaim expired key")
		}
	})
}

func TestEvictKey_RemovesAndAccounts(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	withLock(ks, []string{"a"}, func() {
		e := db.Add("a", []byte("v"))
		db.SetExpire("a", e, baseMs+10)
		if !ks.EvictKey(0, "a") {
			t.Fatal("EvictKey: got false, want true")
		}
		if ks.EvictKey(0, "a") {
			t.Fatal("EvictKey on missing: got true, want false")
		}
		if ks.DBSize(0) != 0 || ks.UsedMemory() != 0 || ks.ExpiresCount(0) != 0 {
			t.Fatalf("size=%d mem=%d exp=%d, want zeros", ks.DBSize(0), ks.UsedMemory(), ks.ExpiresCount(0))
		}
	})
}

func TestFlushDBAndSwapDB(t *testing.T) {
	ks := newKS(t)
	d0 := writeDB(ks, baseMs)
	d1 := &DB{KS: ks, Idx: 1, Now: baseMs, Write: true}
	ks.LockAll(true)
	defer ks.UnlockAll(true)
	for i := range 50 {
		k := fmt.Sprintf("a%d", i)
		e := d0.Add(k, []byte("x"))
		if i%2 == 0 {
			d0.SetExpire(k, e, baseMs+1000)
		}
	}
	for i := range 10 {
		d1.Add(fmt.Sprintf("b%d", i), []byte("y"))
	}
	ks.SwapDB(0, 1)
	if ks.DBSize(0) != 10 || ks.DBSize(1) != 50 || ks.ExpiresCount(0) != 0 || ks.ExpiresCount(1) != 25 {
		t.Fatalf("after swap: sizes %d/%d expires %d/%d", ks.DBSize(0), ks.DBSize(1), ks.ExpiresCount(0), ks.ExpiresCount(1))
	}
	if !d0.Exists("b3") || !d1.Exists("a3") {
		t.Fatal("swap did not move keys")
	}
	memBefore := ks.UsedMemory()
	ks.FlushDB(1)
	if ks.DBSize(1) != 0 || ks.ExpiresCount(1) != 0 || ks.DBSize(0) != 10 {
		t.Fatalf("after FlushDB: sizes %d/%d expires %d", ks.DBSize(0), ks.DBSize(1), ks.ExpiresCount(1))
	}
	if ks.UsedMemory() >= memBefore || ks.UsedMemory() <= 0 {
		t.Fatalf("memory after FlushDB: %d (before %d)", ks.UsedMemory(), memBefore)
	}
	ks.FlushAll()
	if ks.DBSize(0) != 0 || ks.UsedMemory() != 0 {
		t.Fatalf("after FlushAll: size=%d mem=%d", ks.DBSize(0), ks.UsedMemory())
	}
}

func TestIteration_ForEachShardLenSample(t *testing.T) {
	ks := newKS(t)
	db := writeDB(ks, baseMs)
	ks.LockAll(true)
	defer ks.UnlockAll(true)
	for i := range 100 {
		k := fmt.Sprintf("k%d", i)
		e := db.Add(k, []byte("v"))
		if i < 30 {
			db.SetExpire(k, e, baseMs+1)
		}
	}
	var total, vol, visited int
	for i := range ks.NumShards() {
		total += ks.ShardLen(i, 0)
		vol += ks.VolatileLen(i, 0)
		ks.ForEachInShard(i, 0, func(string, *Entry) bool { visited++; return true })
		if n := ks.ShardLen(i, 0); n > 0 {
			ks.ForEachInShard(i, 0, func(string, *Entry) bool { visited--; return false })
			if got := len(ks.SampleShard(i, 0, 1, false)); got != 1 {
				t.Fatalf("SampleShard: got %d, want 1", got)
			}
		}
		for _, k := range ks.SampleShard(i, 0, 100, true) {
			if db.Peek(k).ExpireAt == 0 {
				t.Fatalf("volatile sample returned persistent key %q", k)
			}
		}
	}
	if total != 100 || vol != 30 || visited <= 0 {
		t.Fatalf("total=%d vol=%d visited=%d, want 100/30/>0", total, vol, visited)
	}
}

func TestHoldReleaseSnapshots(t *testing.T) {
	ks := New(2, 1)
	ks.HoldSnapshots()
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		ks.BeginSnapshot(nil) // must block until released
		ks.EndSnapshot()
		close(finished)
	}()
	<-started
	select {
	case <-finished:
		t.Fatal("snapshot started while snapshots were held")
	case <-time.After(50 * time.Millisecond):
	}
	ks.ReleaseSnapshots()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not start after ReleaseSnapshots")
	}
}
