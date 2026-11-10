// Microbenchmarks for shard selection and keyspace reads and writes.
package storage

import (
	"strconv"
	"testing"
)

// benchKeys returns n distinct keys "key:<i>".
func benchKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "key:" + strconv.Itoa(i)
	}
	return keys
}

// shardSink keeps the ShardOf result live so the call cannot be optimised away.
var shardSink int

func BenchmarkShardOf(b *testing.B) {
	ks := New(256, 1)
	keys := benchKeys(1024)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		shardSink = ks.ShardOf(keys[i&1023])
	}
}

func BenchmarkLookup(b *testing.B) {
	ks := New(256, 1)
	db := writeDB(ks, baseMs)
	keys := benchKeys(10_000)
	ks.LockAll(true)
	for _, k := range keys {
		db.Add(k, []byte("v"))
	}
	ks.UnlockAll(true)
	read := &DB{KS: ks, Idx: 0, Now: baseMs}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		k := keys[i%len(keys)]
		sh := ks.ShardOf(k)
		ks.LockShard(sh, false)
		if read.Lookup(k) == nil {
			b.Fatalf("Lookup(%q) = nil", k)
		}
		ks.UnlockShard(sh, false)
	}
}

func BenchmarkSet(b *testing.B) {
	ks := New(256, 1)
	db := writeDB(ks, baseMs)
	keys := benchKeys(10_000)
	val := []byte("value")
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		k := keys[i%len(keys)]
		sh := ks.ShardOf(k)
		ks.LockShard(sh, true)
		db.SetValue(k, val, false)
		ks.UnlockShard(sh, true)
	}
}
