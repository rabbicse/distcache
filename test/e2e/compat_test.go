// go-redis compatibility tests: pipelining, transactions, pub/sub, scripting,
// RESP2/RESP3 decoding and SCAN iteration.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestGoRedisCompat(t *testing.T) {
	ctx := context.Background()
	s, rc := startNode(t, t.TempDir(), nil)

	t.Run("pipeline", func(t *testing.T) {
		pipe := rc.Pipeline()
		gets := make([]*redis.StringCmd, 1000)
		for i := range 1000 {
			pipe.Set(ctx, fmt.Sprint("pl:", i), i, 0)
			gets[i] = pipe.Get(ctx, fmt.Sprint("pl:", i))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatal(err)
		}
		for i, g := range gets {
			if g.Val() != fmt.Sprint(i) {
				t.Fatalf("pl:%d: got %q, want %d", i, g.Val(), i)
			}
		}
	})

	t.Run("tx_pipeline", func(t *testing.T) {
		var incr *redis.IntCmd
		_, err := rc.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.Set(ctx, "tx", 10, 0)
			incr = p.IncrBy(ctx, "tx", 5)
			return nil
		})
		if err != nil || incr.Val() != 15 {
			t.Fatalf("tx: got %d, %v; want 15", incr.Val(), err)
		}
	})

	t.Run("watch_retry", func(t *testing.T) {
		rc.Set(ctx, "w", 0, 0)
		other := redis.NewClient(&redis.Options{Addr: s.Addr()})
		t.Cleanup(func() { _ = other.Close() }) // best-effort: teardown
		attempts := 0
		for {
			attempts++
			err := rc.Watch(ctx, func(tx *redis.Tx) error {
				n, err := tx.Get(ctx, "w").Int()
				if err != nil {
					return err
				}
				if attempts == 1 { // concurrent writer invalidates the first attempt
					other.Incr(ctx, "w")
				}
				_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
					p.Set(ctx, "w", n+100, 0)
					return nil
				})
				return err
			}, "w")
			if errors.Is(err, redis.TxFailedErr) && attempts < 5 {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if attempts != 2 || rc.Get(ctx, "w").Val() != "101" {
			t.Fatalf("got attempts=%d w=%s, want 2 and 101", attempts, rc.Get(ctx, "w").Val())
		}
	})

	t.Run("pubsub", func(t *testing.T) {
		sub := rc.Subscribe(ctx, "ch")
		t.Cleanup(func() { _ = sub.Close() })       // best-effort: teardown
		if _, err := sub.Receive(ctx); err != nil { // subscription confirmation
			t.Fatal(err)
		}
		if n := rc.Publish(ctx, "ch", "hello").Val(); n != 1 {
			t.Fatalf("PUBLISH receivers = %d, want 1", n)
		}
		select {
		case msg := <-sub.Channel():
			if msg.Channel != "ch" || msg.Payload != "hello" {
				t.Fatalf("got %+v, want ch/hello", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no message within 2s")
		}
	})

	t.Run("eval", func(t *testing.T) {
		v, err := rc.Eval(ctx, "redis.call('SET', KEYS[1], ARGV[1]); return redis.call('GET', KEYS[1])", []string{"ev"}, "42").Text()
		if err != nil || v != "42" {
			t.Fatalf("EVAL: got %q, %v; want 42", v, err)
		}
	})

	t.Run("hgetall_resp2", func(t *testing.T) {
		rc.HSet(ctx, "h", "a", "1", "b", "2")
		got := rc.HGetAll(ctx, "h").Val()
		if fmt.Sprint(got) != "map[a:1 b:2]" {
			t.Fatalf("HGETALL: got %v", got)
		}
	})

	t.Run("resp3", func(t *testing.T) {
		r3 := redis.NewClient(&redis.Options{Addr: s.Addr(), Protocol: 3})
		t.Cleanup(func() { _ = r3.Close() }) // best-effort: teardown
		raw, err := r3.Do(ctx, "HGETALL", "h").Result()
		if err != nil {
			t.Fatal(err)
		}
		m, ok := raw.(map[interface{}]interface{})
		if !ok || len(m) != 2 || m["a"] != "1" {
			t.Fatalf("RESP3 HGETALL: got %T %v, want map", raw, raw)
		}
		r3.ZAdd(ctx, "z", redis.Z{Score: 1.5, Member: "x"}, redis.Z{Score: 2, Member: "y"})
		raw, err = r3.Do(ctx, "ZRANGE", "z", 0, -1, "WITHSCORES").Result()
		if err != nil {
			t.Fatal(err)
		}
		pairs, ok := raw.([]interface{})
		if !ok || len(pairs) != 2 {
			t.Fatalf("RESP3 ZRANGE WITHSCORES: got %T %v, want 2 pairs", raw, raw)
		}
		p0, ok := pairs[0].([]interface{})
		if !ok || p0[0] != "x" || p0[1] != 1.5 {
			t.Fatalf("RESP3 pair: got %#v, want [x 1.5] with float score", pairs[0])
		}
		zs := r3.ZRangeWithScores(ctx, "z", 0, -1).Val()
		if len(zs) != 2 || zs[1].Score != 2 || zs[1].Member != "y" {
			t.Fatalf("ZRangeWithScores: got %v", zs)
		}
	})

	t.Run("scan_iterator", func(t *testing.T) {
		for i := range 50 {
			rc.Set(ctx, fmt.Sprint("sc:", i), i, 0)
		}
		var keys []string
		it := rc.Scan(ctx, 0, "sc:*", 7).Iterator()
		for it.Next(ctx) {
			keys = append(keys, it.Val())
		}
		if err := it.Err(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(keys)
		keys = slices.Compact(keys) // SCAN may return a key more than once
		if len(keys) != 50 {
			t.Fatalf("SCAN found %d distinct keys, want 50", len(keys))
		}
	})
}
