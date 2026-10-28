// Package storage implements the sharded, concurrent keyspace.
//
// Unlike Redis, which runs every command on a single thread, DistCache splits
// the keyspace into N shards (power of two, default 256) each guarded by its
// own RWMutex. A command locks only the shards owning its keys (in ascending
// order, so multi-key commands never deadlock), which lets independent
// commands run in parallel on all CPU cores while every individual command,
// MULTI/EXEC block and Lua script stays atomic exactly like in Redis.
//
// All DB methods require the caller to hold the relevant shard locks.
package storage

import (
	"hash/maphash"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/ds"
)

// Value type names as reported by TYPE.
const (
	TypeNone   = "none"
	TypeString = "string"
	TypeList   = "list"
	TypeSet    = "set"
	TypeZSet   = "zset"
	TypeHash   = "hash"
	TypeStream = "stream"
)

// EntryOverhead is the estimated fixed memory cost of a key.
const EntryOverhead = 72

// Entry is a key's value plus metadata.
type Entry struct {
	Value    any   // []byte | *ds.List | *ds.Hash | *ds.Set | *ds.ZSet | *ds.Stream
	ExpireAt int64 // absolute unix ms, 0 = persistent
	Mem      int64 // accounted memory
	lru      atomic.Uint32
	lfu      atomic.Uint32
}

// TypeName returns the Redis type name of the value.
func TypeName(v any) string {
	switch v.(type) {
	case []byte:
		return TypeString
	case *ds.List:
		return TypeList
	case *ds.Set:
		return TypeSet
	case *ds.ZSet:
		return TypeZSet
	case *ds.Hash:
		return TypeHash
	case *ds.Stream:
		return TypeStream
	}
	return TypeNone
}

// ValueBytes returns the estimated memory used by a value.
func ValueBytes(v any) int64 {
	switch x := v.(type) {
	case []byte:
		return int64(cap(x))
	case *ds.List:
		return x.Bytes()
	case *ds.Set:
		return x.Bytes()
	case *ds.ZSet:
		return x.Bytes()
	case *ds.Hash:
		return x.Bytes()
	case *ds.Stream:
		return x.Bytes()
	}
	return 0
}

// CloneValue deep-copies a value.
func CloneValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return append([]byte(nil), x...)
	case *ds.List:
		return x.Clone()
	case *ds.Set:
		return x.Clone()
	case *ds.ZSet:
		return x.Clone()
	case *ds.Hash:
		return x.Clone()
	case *ds.Stream:
		return x.Clone()
	}
	return v
}

// Clone deep-copies an entry.
func (e *Entry) Clone() *Entry {
	n := &Entry{Value: CloneValue(e.Value), ExpireAt: e.ExpireAt, Mem: e.Mem}
	n.lru.Store(e.lru.Load())
	n.lfu.Store(e.lfu.Load())
	return n
}

// LRU returns the last access time (seconds clock).
func (e *Entry) LRU() uint32 { return e.lru.Load() }

// LFUValue returns the packed LFU word.
func (e *Entry) LFUValue() uint32 { return e.lfu.Load() }

// SetLFU sets the packed LFU word.
func (e *Entry) SetLFU(v uint32) { e.lfu.Store(v) }

// SetLRU sets the LRU clock.
func (e *Entry) SetLRU(v uint32) { e.lru.Store(v) }

type dbShard struct {
	keys     map[string]*Entry
	volatile map[string]struct{}
}

// Shard is a lock domain holding a slice of every database.
type Shard struct {
	mu      sync.RWMutex
	dbs     []dbShard
	pending bool
	overlay map[int]map[string]*Entry
}

// Keyspace is the full multi-database dataset.
type Keyspace struct {
	shards  []*Shard
	mask    uint64
	NumDB   int
	seed    maphash.Seed
	used    atomic.Int64
	sizes   []atomic.Int64
	expires []atomic.Int64

	snapMu     sync.Mutex
	snapActive atomic.Bool
	snapDone   chan struct{}

	// Clock is the LRU clock in seconds, updated by the server cron.
	Clock atomic.Uint32
	// LFULogFactor and LFUDecayTime mirror the config values.
	LFULogFactor atomic.Int64
	LFUDecayTime atomic.Int64
}

// New creates a keyspace with nshards shards (power of two) and ndb DBs.
func New(nshards, ndb int) *Keyspace {
	if nshards < 1 || nshards&(nshards-1) != 0 {
		nshards = 256
	}
	ks := &Keyspace{
		shards:  make([]*Shard, nshards),
		mask:    uint64(nshards - 1),
		NumDB:   ndb,
		seed:    maphash.MakeSeed(),
		sizes:   make([]atomic.Int64, ndb),
		expires: make([]atomic.Int64, ndb),
	}
	for i := range ks.shards {
		sh := &Shard{dbs: make([]dbShard, ndb)}
		for d := range sh.dbs {
			sh.dbs[d] = dbShard{keys: make(map[string]*Entry), volatile: make(map[string]struct{})}
		}
		ks.shards[i] = sh
	}
	ks.Clock.Store(uint32(time.Now().Unix()))
	ks.LFULogFactor.Store(10)
	ks.LFUDecayTime.Store(1)
	return ks
}

// NumShards returns the shard count.
func (ks *Keyspace) NumShards() int { return len(ks.shards) }

// ShardOf returns the shard index for a key.
func (ks *Keyspace) ShardOf(key string) int {
	return int(maphash.String(ks.seed, key) & ks.mask)
}

// ShardSet computes the sorted unique shard indexes for keys.
func (ks *Keyspace) ShardSet(keys []string, dst []int) []int {
	dst = dst[:0]
	for _, k := range keys {
		dst = append(dst, ks.ShardOf(k))
	}
	if len(dst) <= 1 {
		return dst
	}
	sort.Ints(dst)
	out := dst[:1]
	for _, v := range dst[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// Lock acquires shard locks (idx sorted ascending).
func (ks *Keyspace) Lock(idx []int, write bool) {
	for _, i := range idx {
		if write {
			ks.shards[i].mu.Lock()
		} else {
			ks.shards[i].mu.RLock()
		}
	}
}

// Unlock releases shard locks.
func (ks *Keyspace) Unlock(idx []int, write bool) {
	for j := len(idx) - 1; j >= 0; j-- {
		if write {
			ks.shards[idx[j]].mu.Unlock()
		} else {
			ks.shards[idx[j]].mu.RUnlock()
		}
	}
}

// LockAll locks every shard.
func (ks *Keyspace) LockAll(write bool) {
	for _, sh := range ks.shards {
		if write {
			sh.mu.Lock()
		} else {
			sh.mu.RLock()
		}
	}
}

// UnlockAll unlocks every shard.
func (ks *Keyspace) UnlockAll(write bool) {
	for j := len(ks.shards) - 1; j >= 0; j-- {
		if write {
			ks.shards[j].mu.Unlock()
		} else {
			ks.shards[j].mu.RUnlock()
		}
	}
}

// LockShard locks a single shard.
func (ks *Keyspace) LockShard(i int, write bool) {
	if write {
		ks.shards[i].mu.Lock()
	} else {
		ks.shards[i].mu.RLock()
	}
}

// UnlockShard unlocks a single shard.
func (ks *Keyspace) UnlockShard(i int, write bool) {
	if write {
		ks.shards[i].mu.Unlock()
	} else {
		ks.shards[i].mu.RUnlock()
	}
}

// UsedMemory returns the accounted dataset memory in bytes.
func (ks *Keyspace) UsedMemory() int64 { return ks.used.Load() }

// DBSize returns the number of keys in db (including logically expired keys
// not yet reclaimed, like Redis).
func (ks *Keyspace) DBSize(db int) int64 { return ks.sizes[db].Load() }

// ExpiresCount returns keys with a TTL in db.
func (ks *Keyspace) ExpiresCount(db int) int64 { return ks.expires[db].Load() }

// ---- LFU helpers (ported from Redis evict.c) ----

const lfuInitVal = 5

func lfuMinutes() uint32 { return uint32(time.Now().Unix()/60) & 65535 }

func (ks *Keyspace) lfuDecr(word uint32) uint32 {
	ldt := word >> 8
	counter := word & 255
	now := lfuMinutes()
	var elapsed uint32
	if now >= ldt {
		elapsed = now - ldt
	} else {
		elapsed = 65535 - ldt + now
	}
	dt := uint32(ks.LFUDecayTime.Load())
	if dt > 0 {
		periods := elapsed / dt
		if periods > counter {
			counter = 0
		} else {
			counter -= periods
		}
	}
	return counter
}

// LFUCounter returns the decayed frequency counter.
func (ks *Keyspace) LFUCounter(e *Entry) uint32 { return ks.lfuDecr(e.lfu.Load()) }

func (ks *Keyspace) touch(e *Entry) {
	e.lru.Store(ks.Clock.Load())
	counter := ks.lfuDecr(e.lfu.Load())
	if counter < 255 {
		base := float64(counter) - lfuInitVal
		if base < 0 {
			base = 0
		}
		p := 1.0 / (base*float64(ks.LFULogFactor.Load()) + 1)
		if rand.Float64() < p {
			counter++
		}
	}
	e.lfu.Store(lfuMinutes()<<8 | counter)
}

// NewEntry creates an entry with initialised access metadata.
func (ks *Keyspace) NewEntry(v any) *Entry {
	e := &Entry{Value: v}
	e.lru.Store(ks.Clock.Load())
	e.lfu.Store(lfuMinutes()<<8 | lfuInitVal)
	return e
}

// ---- DB view ----

// ExpireHook is invoked when a key is lazily or actively expired.
type ExpireHook func(db int, key string)

// DB is a view over one logical database for the duration of a command.
// The caller must hold the shard locks for every key it touches; Write must
// be true only when those locks are exclusive.
type DB struct {
	KS    *Keyspace
	Idx   int
	Now   int64
	Write bool
	// NoTouch disables LRU/LFU updates (CLIENT NO-TOUCH, OBJECT, TYPE...).
	NoTouch bool
	// Replica mode: keys never expire on access; master client sees all.
	ReplicaMode  bool
	IgnoreExpiry bool
	OnExpire     ExpireHook
}

func (db *DB) shard(key string) *Shard { return db.KS.shards[db.KS.ShardOf(key)] }

func (db *DB) part(key string) (*Shard, *dbShard) {
	sh := db.shard(key)
	return sh, &sh.dbs[db.Idx]
}

// IsExpired reports whether e is past its TTL.
func (db *DB) IsExpired(e *Entry) bool {
	return e.ExpireAt != 0 && e.ExpireAt <= db.Now && !db.IgnoreExpiry
}

// Peek returns the entry without expiry handling or access tracking.
func (db *DB) Peek(key string) *Entry {
	_, p := db.part(key)
	return p.keys[key]
}

// Lookup returns the live entry for key or nil, reclaiming it if expired
// (when holding a write lock) and updating access statistics.
func (db *DB) Lookup(key string) *Entry {
	_, p := db.part(key)
	e := p.keys[key]
	if e == nil {
		return nil
	}
	if db.IsExpired(e) {
		if db.Write && !db.ReplicaMode {
			db.Delete(key)
			if db.OnExpire != nil {
				db.OnExpire(db.Idx, key)
			}
		}
		return nil
	}
	if !db.NoTouch {
		db.KS.touch(e)
	}
	return e
}

// Exists reports whether key is live (no access tracking).
func (db *DB) Exists(key string) bool {
	_, p := db.part(key)
	e := p.keys[key]
	return e != nil && !db.IsExpired(e)
}

// Set stores e under key, replacing any previous value and TTL.
func (db *DB) Set(key string, e *Entry) {
	_, p := db.part(key)
	old := p.keys[key]
	e.Mem = EntryOverhead + int64(len(key)) + ValueBytes(e.Value)
	if old != nil {
		db.KS.used.Add(e.Mem - old.Mem)
		if old.ExpireAt != 0 {
			delete(p.volatile, key)
			db.KS.expires[db.Idx].Add(-1)
		}
	} else {
		db.KS.used.Add(e.Mem)
		db.KS.sizes[db.Idx].Add(1)
	}
	p.keys[key] = e
	if e.ExpireAt != 0 {
		p.volatile[key] = struct{}{}
		db.KS.expires[db.Idx].Add(1)
	}
}

// Add stores a fresh value without TTL and returns its entry.
func (db *DB) Add(key string, v any) *Entry {
	e := db.KS.NewEntry(v)
	db.Set(key, e)
	return e
}

// SetValue replaces the value of key keeping its TTL when keepTTL is true.
func (db *DB) SetValue(key string, v any, keepTTL bool) *Entry {
	var at int64
	if keepTTL {
		if old := db.Lookup(key); old != nil {
			at = old.ExpireAt
		}
	}
	e := db.KS.NewEntry(v)
	e.ExpireAt = at
	db.Set(key, e)
	return e
}

// Delete removes key; returns true if it existed (even if expired).
func (db *DB) Delete(key string) bool {
	_, p := db.part(key)
	e, ok := p.keys[key]
	if !ok {
		return false
	}
	delete(p.keys, key)
	db.KS.used.Add(-e.Mem)
	db.KS.sizes[db.Idx].Add(-1)
	if e.ExpireAt != 0 {
		delete(p.volatile, key)
		db.KS.expires[db.Idx].Add(-1)
	}
	return true
}

// SetExpire sets the absolute expiry of an existing entry (0 = persist).
func (db *DB) SetExpire(key string, e *Entry, at int64) {
	_, p := db.part(key)
	if e.ExpireAt == 0 && at != 0 {
		p.volatile[key] = struct{}{}
		db.KS.expires[db.Idx].Add(1)
	} else if e.ExpireAt != 0 && at == 0 {
		delete(p.volatile, key)
		db.KS.expires[db.Idx].Add(-1)
	}
	e.ExpireAt = at
}

// Updated recomputes memory accounting for key after an in-place mutation.
func (db *DB) Updated(key string) {
	_, p := db.part(key)
	e := p.keys[key]
	if e == nil {
		return
	}
	m := EntryOverhead + int64(len(key)) + ValueBytes(e.Value)
	if m != e.Mem {
		db.KS.used.Add(m - e.Mem)
		e.Mem = m
	}
}

// Preserve records the pre-modification state of key for an in-progress
// point-in-time snapshot (copy-on-write). Requires the write lock.
func (db *DB) Preserve(key string) {
	if !db.KS.snapActive.Load() {
		return
	}
	sh, p := db.part(key)
	sh.preserve(db.Idx, key, p)
}

func (sh *Shard) preserve(db int, key string, p *dbShard) {
	if !sh.pending {
		return
	}
	m := sh.overlay[db]
	if m == nil {
		m = make(map[string]*Entry)
		sh.overlay[db] = m
	}
	if _, done := m[key]; done {
		return
	}
	if e := p.keys[key]; e != nil {
		m[key] = e.Clone()
	} else {
		m[key] = nil
	}
}

// ---- whole-keyspace helpers (caller holds all relevant locks) ----

// Flush removes all keys of db in shard i.
func (ks *Keyspace) flushShard(i, db int) {
	sh := ks.shards[i]
	p := &sh.dbs[db]
	if sh.pending {
		for k := range p.keys {
			sh.preserve(db, k, p)
		}
	}
	var mem int64
	for _, e := range p.keys {
		mem += e.Mem
	}
	ks.used.Add(-mem)
	ks.sizes[db].Add(-int64(len(p.keys)))
	ks.expires[db].Add(-int64(len(p.volatile)))
	p.keys = make(map[string]*Entry)
	p.volatile = make(map[string]struct{})
}

// FlushDB empties db. Caller must hold all shard write locks.
func (ks *Keyspace) FlushDB(db int) {
	for i := range ks.shards {
		ks.flushShard(i, db)
	}
}

// FlushAll empties every db. Caller must hold all shard write locks.
func (ks *Keyspace) FlushAll() {
	for d := 0; d < ks.NumDB; d++ {
		ks.FlushDB(d)
	}
}

// SwapDB swaps two databases. Caller must hold all shard write locks and no
// snapshot may be active.
func (ks *Keyspace) SwapDB(a, b int) {
	for _, sh := range ks.shards {
		sh.dbs[a], sh.dbs[b] = sh.dbs[b], sh.dbs[a]
	}
	sa, sb := ks.sizes[a].Load(), ks.sizes[b].Load()
	ks.sizes[a].Store(sb)
	ks.sizes[b].Store(sa)
	ea, eb := ks.expires[a].Load(), ks.expires[b].Load()
	ks.expires[a].Store(eb)
	ks.expires[b].Store(ea)
}

// ForEachInShard iterates keys of db in shard i (caller holds the lock).
func (ks *Keyspace) ForEachInShard(i, db int, fn func(key string, e *Entry) bool) {
	for k, e := range ks.shards[i].dbs[db].keys {
		if !fn(k, e) {
			return
		}
	}
}

// ShardLen returns the number of keys of db in shard i.
func (ks *Keyspace) ShardLen(i, db int) int { return len(ks.shards[i].dbs[db].keys) }

// SampleShard returns up to n keys from shard i/db, starting at a random
// position (Go randomises map iteration). volatileOnly samples keys with TTL.
func (ks *Keyspace) SampleShard(i, db, n int, volatileOnly bool) []string {
	p := &ks.shards[i].dbs[db]
	out := make([]string, 0, n)
	if volatileOnly {
		for k := range p.volatile {
			out = append(out, k)
			if len(out) >= n {
				break
			}
		}
		return out
	}
	for k := range p.keys {
		out = append(out, k)
		if len(out) >= n {
			break
		}
	}
	return out
}

// VolatileLen returns keys with TTL of db in shard i.
func (ks *Keyspace) VolatileLen(i, db int) int { return len(ks.shards[i].dbs[db].volatile) }

// ExpireShard actively expires up to sample volatile keys in shard i. The
// caller must hold the shard write lock. Returns (sampled, expired).
func (ks *Keyspace) ExpireShard(i, db, sample int, now int64, hook ExpireHook) (int, int) {
	sh := ks.shards[i]
	p := &sh.dbs[db]
	sampled, expired := 0, 0
	var victims []string
	for k := range p.volatile {
		sampled++
		if e := p.keys[k]; e != nil && e.ExpireAt <= now {
			victims = append(victims, k)
		}
		if sampled >= sample {
			break
		}
	}
	for _, k := range victims {
		sh.preserve(db, k, p)
		v := DB{KS: ks, Idx: db, Now: now, Write: true}
		v.Delete(k)
		expired++
		if hook != nil {
			hook(db, k)
		}
	}
	return sampled, expired
}

// EvictKey deletes key (caller holds the shard write lock).
func (ks *Keyspace) EvictKey(db int, key string) bool {
	sh := ks.shards[ks.ShardOf(key)]
	p := &sh.dbs[db]
	sh.preserve(db, key, p)
	v := DB{KS: ks, Idx: db, Write: true}
	return v.Delete(key)
}

// ---- point-in-time snapshots (copy-on-write) ----

// BeginSnapshot starts a point-in-time snapshot. cut is invoked while every
// shard is write-locked, so it observes a consistent instant (used to fix
// replication / AOF offsets). Only one snapshot runs at a time; callers
// queue on snapMu.
func (ks *Keyspace) BeginSnapshot(cut func()) {
	ks.snapMu.Lock()
	ks.LockAll(true)
	for _, sh := range ks.shards {
		sh.pending = true
		sh.overlay = make(map[int]map[string]*Entry)
	}
	ks.snapDone = make(chan struct{})
	ks.snapActive.Store(true)
	if cut != nil {
		cut()
	}
	ks.UnlockAll(true)
}

// SnapshotShard visits the frozen view of shard i: fn is called for every
// (db, key, entry) as it was at BeginSnapshot. The shard is locked only while
// collecting; fn runs without locks on cloned or detached data, so it must
// not mutate entries.
func (ks *Keyspace) SnapshotShard(i int, fn func(db int, key string, e *Entry)) {
	sh := ks.shards[i]
	type item struct {
		db  int
		key string
		e   *Entry
	}
	sh.mu.Lock()
	var items []item
	for d := range sh.dbs {
		ov := sh.overlay[d]
		for k, e := range sh.dbs[d].keys {
			if ov != nil {
				if pe, ok := ov[k]; ok {
					if pe != nil {
						items = append(items, item{d, k, pe})
					}
					continue
				}
			}
			items = append(items, item{d, k, e.Clone()})
		}
		for k, pe := range ov {
			if pe == nil {
				continue
			}
			if _, live := sh.dbs[d].keys[k]; !live {
				items = append(items, item{d, k, pe})
			}
		}
	}
	sh.pending = false
	sh.overlay = nil
	sh.mu.Unlock()
	for _, it := range items {
		fn(it.db, it.key, it.e)
	}
}

// EndSnapshot finishes the snapshot started by BeginSnapshot.
func (ks *Keyspace) EndSnapshot() {
	for _, sh := range ks.shards {
		sh.mu.Lock()
		sh.pending = false
		sh.overlay = nil
		sh.mu.Unlock()
	}
	ks.snapActive.Store(false)
	close(ks.snapDone)
	ks.snapMu.Unlock()
}

// SnapshotActive reports whether a snapshot is running.
func (ks *Keyspace) SnapshotActive() bool { return ks.snapActive.Load() }

// HoldSnapshots waits for any running snapshot to finish and prevents a new
// one from starting until ReleaseSnapshots. Commands that may touch keys they
// did not declare (FLUSHALL, SWAPDB, scripts) run inside this barrier so the
// copy-on-write bookkeeping never misses a mutation. Lock order is always
// snapshot barrier first, then shards.
func (ks *Keyspace) HoldSnapshots() { ks.snapMu.Lock() }

// ReleaseSnapshots ends HoldSnapshots.
func (ks *Keyspace) ReleaseSnapshots() { ks.snapMu.Unlock() }
