// Read-only statistics API consumed by the web console dashboard. It reads
// the same atomics INFO reports so the two never disagree.
package server

import (
	"time"

	"github.com/rabbicse/distcache/internal/storage"
)

// ShardStat is one shard's statistics in a StatsSnapshot.
type ShardStat struct {
	Keys     int64         // keys across every database in the shard
	// Ops counts top-level keyed commands that locked the shard. Commands that
	// lock every shard (FLUSHALL, EVAL, MULTI/EXEC...), commands queued in a
	// transaction or run from a script, and keyspace walks such as KEYS/SCAN
	// are not counted per shard; they still count in StatsSnapshot.Commands.
	Ops int64
	LockWait time.Duration // total time spent blocked acquiring the shard lock
}

// StatsSnapshot is a point-in-time view of server statistics.
type StatsSnapshot struct {
	Time       time.Time
	OpsPerSec  int64
	Commands   int64
	Clients    int64
	UsedMemory int64
	MaxMemory  int64
	Hits       int64
	Misses     int64
	Evicted    int64
	Expired    int64
	NetIn      int64
	NetOut     int64
	Role       string // "master" | "replica"
	Shards     []ShardStat
}

// Stats returns a snapshot of server statistics. Shards are read one at a
// time, so per-shard values are not mutually consistent, but no global lock
// is taken.
func (s *Server) Stats() StatsSnapshot {
	role := "master"
	if s.replication.isReplica() {
		role = "replica"
	}
	counters := s.keyspace.ShardStats(make([]storage.ShardCounters, 0, s.keyspace.NumShards()))
	shards := make([]ShardStat, len(counters))
	for i, sc := range counters {
		shards[i] = ShardStat{Keys: sc.Keys, Ops: sc.Ops, LockWait: time.Duration(sc.LockWaitNanos)}
	}
	return StatsSnapshot{
		Time:       s.Now(),
		OpsPerSec:  s.stats.instantaneousOps.Load(),
		Commands:   s.stats.commands.Load(),
		Clients:    int64(s.clientCount()),
		UsedMemory: s.keyspace.UsedMemory(),
		MaxMemory:  s.cfg.MaxMemory.Load(),
		Hits:       s.stats.keyspaceHits.Load(),
		Misses:     s.stats.keyspaceMisses.Load(),
		Evicted:    s.stats.evictedKeys.Load(),
		Expired:    s.stats.expiredKeys.Load(),
		NetIn:      s.stats.netInput.Load(),
		NetOut:     s.stats.netOutput.Load(),
		Role:       role,
		Shards:     shards,
	}
}
