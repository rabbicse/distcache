// Command example runs a short tour of common Redis features against a
// DistCache server using the go-redis client. It prints "OK <step>" per check.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	addr := os.Getenv("DISTCACHE_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	check := func(step string, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", step, err)
			os.Exit(1)
		}
		fmt.Println("OK", step)
	}
	expect := func(got, want any) error {
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("got %v, want %v", got, want)
		}
		return nil
	}

	check("ping", rdb.Ping(ctx).Err())

	rdb.Del(ctx, "ex:str", "ex:hash", "ex:list", "ex:set", "ex:zset", "ex:counter")
	err := rdb.Set(ctx, "ex:str", "hello", 10*time.Second).Err()
	if err == nil {
		err = expect(rdb.Get(ctx, "ex:str").Val(), "hello")
	}
	if err == nil && rdb.TTL(ctx, "ex:str").Val() <= 0 {
		err = fmt.Errorf("missing TTL")
	}
	check("string+ttl", err)

	rdb.HSet(ctx, "ex:hash", "name", "ann", "age", "30")
	rdb.RPush(ctx, "ex:list", "a", "b", "c")
	rdb.SAdd(ctx, "ex:set", "x", "y", "x")
	rdb.ZAdd(ctx, "ex:zset", redis.Z{Score: 2, Member: "bob"}, redis.Z{Score: 1, Member: "amy"})
	err = expect(rdb.HGet(ctx, "ex:hash", "name").Val(), "ann")
	if err == nil {
		err = expect(rdb.LRange(ctx, "ex:list", 0, -1).Val(), []string{"a", "b", "c"})
	}
	if err == nil {
		err = expect(rdb.SCard(ctx, "ex:set").Val(), 2)
	}
	if err == nil {
		err = expect(rdb.ZRange(ctx, "ex:zset", 0, -1).Val(), []string{"amy", "bob"})
	}
	check("data types", err)

	_, err = rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for range 100 {
			p.Incr(ctx, "ex:counter")
		}
		return nil
	})
	if err == nil {
		err = expect(rdb.Get(ctx, "ex:counter").Val(), "100")
	}
	check("pipeline", err)

	cmds, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Incr(ctx, "ex:counter")
		p.Incr(ctx, "ex:counter")
		return nil
	})
	if err == nil {
		err = expect(cmds[1].(*redis.IntCmd).Val(), 102)
	}
	check("multi/exec", err)

	v, err := rdb.Eval(ctx, "return redis.call('INCRBY', KEYS[1], ARGV[1])", []string{"ex:counter"}, 8).Result()
	if err == nil {
		err = expect(v, 110)
	}
	check("lua", err)

	sub := rdb.Subscribe(ctx, "ex:news")
	defer sub.Close()
	if _, err = sub.Receive(ctx); err == nil {
		rdb.Publish(ctx, "ex:news", "hi")
		var msg *redis.Message
		msg, err = sub.ReceiveMessage(ctx)
		if err == nil {
			err = expect(msg.Payload, "hi")
		}
	}
	check("pubsub", err)
}
