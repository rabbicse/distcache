package storage

import (
	"fmt"
	"testing"
	"time"
)

func TestLazyExpiry(t *testing.T) {
	ks := newKS(t)
	var calls []string
	withLock(ks, []string{"k"}, func() {
		w := writeDB(ks, baseMs)
		e := w.Add("k", []byte("v"))
		w.SetExpire("k", e, baseMs+100)
		if w.Lookup("k") == nil {
			t.Fatal("key should be live before expiry")
		}
		r := &DB{KS: ks, Idx: 0, Now: baseMs + 101, Write: true,
			OnExpire: func(db int, key string) { calls = append(calls, fmt.Sprintf("%d:%s", db, key)) }}
		if got := r.Lookup("k"); got != nil {
			t.Fatalf("Lookup: got %v, want nil", got)
		}
		if len(calls) != 1 || calls[0] != "0:k" {
			t.Fatalf("OnExpire calls: got %v, want [0:k]", calls)
		}
		if ks.ExpiresCount(0) != 0 || ks.DBSize(0) != 0 {
			t.Fatalf("expires=%d size=%d, want 0/0", ks.ExpiresCount(0), ks.DBSize(0))
		}
		if r.Lookup("k") != nil || len(calls) != 1 {
			t.Fatal("second lookup must not fire hook again")
		}
	})
}

func TestExpireShardSampling(t *testing.T) {
	ks := New(1, 1)
	ks.SetClock(func() time.Time { return time.UnixMilli(baseMs) })
	db := writeDB(ks, baseMs)
	ks.LockAll(true)
	defer ks.UnlockAll(true)
	for i := range 100 {
		k := fmt.Sprintf("v%d", i)
		e := db.Add(k, []byte("x"))
		db.SetExpire(k, e, baseMs+10)
	}
	var hooked int
	hook := func(int, string) { hooked++ }
	sampled, expired := ks.ExpireShard(0, 0, 20, baseMs+1000, hook)
	if sampled != 20 || expired != 20 {
		t.Fatalf("first call: got sampled=%d expired=%d, want 20/20", sampled, expired)
	}
	if hooked != 20 || ks.DBSize(0) != 80 || ks.ExpiresCount(0) != 80 {
		t.Fatalf("hooked=%d size=%d expires=%d, want 20/80/80", hooked, ks.DBSize(0), ks.ExpiresCount(0))
	}
	for ks.DBSize(0) > 0 {
		if _, n := ks.ExpireShard(0, 0, 20, baseMs+1000, nil); n == 0 {
			t.Fatal("no progress while expired keys remain")
		}
	}
	if ks.UsedMemory() != 0 || ks.VolatileLen(0, 0) != 0 {
		t.Fatalf("mem=%d volatile=%d, want 0/0", ks.UsedMemory(), ks.VolatileLen(0, 0))
	}
}

func TestExpireShard_LeavesLiveKeys(t *testing.T) {
	ks := New(1, 1)
	db := writeDB(ks, baseMs)
	ks.LockAll(true)
	defer ks.UnlockAll(true)
	e := db.Add("live", []byte("x"))
	db.SetExpire("live", e, baseMs+10_000)
	sampled, expired := ks.ExpireShard(0, 0, 20, baseMs, nil)
	if sampled != 1 || expired != 0 || ks.DBSize(0) != 1 {
		t.Fatalf("sampled=%d expired=%d size=%d, want 1/0/1", sampled, expired, ks.DBSize(0))
	}
}

func TestLRU_TouchAndNoTouch(t *testing.T) {
	ks := newKS(t)
	ks.Clock.Store(100)
	withLock(ks, []string{"k"}, func() {
		db := writeDB(ks, baseMs)
		e := db.Add("k", []byte("v"))
		if e.LRU() != 100 {
			t.Fatalf("NewEntry LRU: got %d, want 100", e.LRU())
		}
		ks.Clock.Store(200)
		nt := &DB{KS: ks, Now: baseMs, Write: true, NoTouch: true}
		nt.Lookup("k")
		if e.LRU() != 100 {
			t.Fatalf("NoTouch changed LRU: got %d, want 100", e.LRU())
		}
		db.Lookup("k")
		if e.LRU() != 200 {
			t.Fatalf("Lookup LRU: got %d, want 200", e.LRU())
		}
		db.Peek("k")
		ks.Clock.Store(300)
		if db.Exists("k"); e.LRU() != 200 {
			t.Fatalf("Exists/Peek must not touch: got %d, want 200", e.LRU())
		}
	})
}

func TestLFU_IncrementsToCap(t *testing.T) {
	ks := newKS(t)
	ks.LFULogFactor.Store(0)
	withLock(ks, []string{"k"}, func() {
		db := writeDB(ks, baseMs)
		e := db.Add("k", []byte("v"))
		if got := ks.LFUCounter(e); got != lfuInitVal {
			t.Fatalf("initial counter: got %d, want %d", got, lfuInitVal)
		}
		// With log factor 0 the increment probability is 1, so it is deterministic.
		for range 300 {
			db.Lookup("k")
		}
		if got := ks.LFUCounter(e); got != 255 {
			t.Fatalf("counter: got %d, want 255", got)
		}
	})
}

func TestLFU_Decay(t *testing.T) {
	ks := New(2, 1)
	now := time.UnixMilli(baseMs)
	ks.SetClock(func() time.Time { return now })
	ks.LFULogFactor.Store(0)
	ks.LFUDecayTime.Store(1)
	e := ks.NewEntry([]byte("v"))
	e.SetLFU(ks.lfuMinutes()<<8 | 50)

	now = now.Add(2 * time.Minute)
	if got := ks.LFUCounter(e); got != 48 {
		t.Fatalf("after 2min decay=1: got %d, want 48", got)
	}
	ks.LFUDecayTime.Store(2)
	if got := ks.LFUCounter(e); got != 49 {
		t.Fatalf("after 2min decay=2: got %d, want 49", got)
	}
	now = now.Add(24 * time.Hour)
	if got := ks.LFUCounter(e); got != 0 {
		t.Fatalf("long idle: got %d, want 0 (floor)", got)
	}
	ks.LFUDecayTime.Store(0) // decay disabled
	if got := ks.LFUCounter(e); got != 50 {
		t.Fatalf("decay disabled: got %d, want 50", got)
	}
}

func TestLFU_MinutesWraparound(t *testing.T) {
	ks := New(2, 1)
	// Pick a time where minutes&65535 is just below the wrap point.
	minutes := int64(65535) + 65536*10
	now := time.Unix(minutes*60, 0)
	ks.SetClock(func() time.Time { return now })
	e := ks.NewEntry([]byte("v"))
	e.SetLFU(ks.lfuMinutes()<<8 | 20)
	now = now.Add(3 * time.Minute) // crosses the 16-bit boundary
	// Redis' LFUTimeElapsed (65535-ldt+now) counts one minute fewer across the
	// wrap; this is a faithful port, so 3 real minutes decay by 2.
	if got := ks.LFUCounter(e); got != 18 {
		t.Fatalf("wraparound decay: got %d, want 18", got)
	}
}

func TestEntry_CloneKeepsMetadata(t *testing.T) {
	e := &Entry{Value: []byte("v"), ExpireAt: 5, Mem: 9}
	e.SetLRU(7)
	e.SetLFU(11)
	c := e.Clone()
	if c.ExpireAt != 5 || c.Mem != 9 || c.LRU() != 7 || c.LFUValue() != 11 {
		t.Fatalf("clone mismatch: %+v", c)
	}
}
