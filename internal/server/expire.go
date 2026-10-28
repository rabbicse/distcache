package server

import (
	"errors"
	"math/rand/v2"
	"net"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/storage"
)

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// activeExpireCycle samples volatile keys shard by shard, like Redis'
// activeExpireCycle, within a ~25% CPU time budget per cron tick. Because
// each shard is locked individually, clients keep running on other shards.
func (s *Server) activeExpireCycle(cursor int) int {
	hz := s.cfg.Hz.Load()
	budget := time.Second / time.Duration(hz) / 4
	start := time.Now()
	n := s.ks.NumShards()
	visited := 0
	hook := func(db int, key string) { s.onKeyExpired(db, key) }
	for visited < n && time.Since(start) < budget {
		i := cursor % n
		cursor++
		visited++
		for db := 0; db < s.ks.NumDB; db++ {
			if s.ks.ExpiresCount(db) == 0 {
				continue
			}
			for {
				s.ks.LockShard(i, true)
				if s.ks.VolatileLen(i, db) == 0 {
					s.ks.UnlockShard(i, true)
					break
				}
				sampled, expired := s.ks.ExpireShard(i, db, 20, time.Now().UnixMilli(), hook)
				s.ks.UnlockShard(i, true)
				if sampled == 0 || expired*4 <= sampled || time.Since(start) >= budget {
					break
				}
			}
		}
	}
	return cursor % n
}

// performEvictions frees memory according to maxmemory-policy. Returns false
// when memory is still above the limit (the command must be refused).
func (s *Server) performEvictions() bool {
	max := s.cfg.MaxMemory.Load()
	if max <= 0 || s.ks.UsedMemory() <= max {
		return true
	}
	policy := s.cfg.MaxMemoryPolicy.Load()
	if policy == config.PolicyNoEviction {
		return false
	}
	volatile := policy >= config.PolicyVolatileLRU
	samples := int(s.cfg.MaxMemorySamples.Load())
	n := s.ks.NumShards()
	deadline := time.Now().Add(50 * time.Millisecond)
	for s.ks.UsedMemory() > max {
		if time.Now().After(deadline) {
			return s.ks.UsedMemory() <= max
		}
		type cand struct {
			db    int
			key   string
			score float64
		}
		var best *cand
		// Sample a few random shards for good approximation.
		for attempt := 0; attempt < 4; attempt++ {
			i := rand.IntN(n)
			s.ks.LockShard(i, false)
			for db := 0; db < s.ks.NumDB; db++ {
				if s.ks.DBSize(db) == 0 || (volatile && s.ks.ExpiresCount(db) == 0) {
					continue
				}
				for _, k := range s.ks.SampleShard(i, db, samples, volatile) {
					d := storage.DB{KS: s.ks, Idx: db}
					e := d.Peek(k)
					if e == nil {
						continue
					}
					var score float64
					switch policy {
					case config.PolicyAllKeysLRU, config.PolicyVolatileLRU:
						score = float64(s.ks.Clock.Load() - e.LRU())
					case config.PolicyAllKeysLFU, config.PolicyVolatileLFU:
						score = 255 - float64(s.ks.LFUCounter(e))
					case config.PolicyVolatileTTL:
						score = -float64(e.ExpireAt)
					default:
						score = rand.Float64()
					}
					if best == nil || score > best.score {
						best = &cand{db, k, score}
					}
				}
			}
			s.ks.UnlockShard(i, false)
		}
		if best == nil {
			// Nothing sampled: check whether anything is evictable at all.
			any := false
			for db := 0; db < s.ks.NumDB; db++ {
				if (!volatile && s.ks.DBSize(db) > 0) || (volatile && s.ks.ExpiresCount(db) > 0) {
					any = true
				}
			}
			if !any {
				return false
			}
			continue
		}
		i := s.ks.ShardOf(best.key)
		s.ks.LockShard(i, true)
		if s.ks.EvictKey(best.db, best.key) {
			s.stats.evictedKeys.Add(1)
			s.prop.propagate([]propCmd{{db: best.db, args: [][]byte{[]byte("DEL"), []byte(best.key)}}}, false)
			s.watches.touch(best.db, []string{best.key})
			s.notify(config.NotifyEvicted, "evicted", best.key, best.db)
		}
		s.ks.UnlockShard(i, true)
	}
	return true
}
