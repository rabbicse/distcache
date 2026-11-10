# Guide 1 — Trying DistCache by hand

This guide walks you through building DistCache, starting it, and checking each
major feature yourself: data types, expiry, transactions, pub/sub, Lua,
persistence, crash recovery, replication and load. Every step lists the reply
you should see.

Prerequisites: Go 1.27.3 and macOS or Linux. A Redis client is optional (see step 2).

## 1. Build and start the server

```sh
git clone https://github.com/rabbicse/distcache && cd distcache
make build                       # writes bin/distcache-server and bin/distcache-bench
mkdir -p /tmp/dc1
./bin/distcache-server --port 6379 --dir /tmp/dc1
```

Leave this terminal running. Open a second terminal for the rest of the guide.

## 2. Connect a client

DistCache uses the Redis protocol (RESP), so any Redis client works. Pick one:

| Option | Command |
|---|---|
| `redis-cli` from Homebrew | `brew install redis`, then run `redis-cli -p 6379` |
| `redis-cli` from Docker | `docker run --rm -it --network host redis:8.4.8 redis-cli -p 6379` |
| No install at all | `nc 127.0.0.1 6379`, then type commands such as `PING` and press Enter |

The examples below are written for `redis-cli`. With `nc` you see raw RESP replies
instead: `+OK` for a status, `$1` followed by `v` for a bulk string, and `:1` for an integer.

```text
> PING
PONG
> INFO server
# Server
redis_version:...
distcache_version:1.0.0
```

## 3. Data types

```text
> SET user:1 alice            → OK
> GET user:1                  → "alice"
> INCR visits                 → (integer) 1
> LPUSH queue a b c           → (integer) 3
> LRANGE queue 0 -1           → 1) "c" 2) "b" 3) "a"
> HSET u:1 name ann age 30    → (integer) 2
> HGETALL u:1                 → name ann age 30
> SADD tags go redis go       → (integer) 2
> ZADD board 10 amy 20 bob    → (integer) 2
> ZRANGE board 0 -1 WITHSCORES→ amy 10 bob 20
> XADD events * type login    → "1731234567890-0"
> PFADD visitors a b c        → (integer) 1
> PFCOUNT visitors            → (integer) 3
> GEOADD city 13.36 38.11 palermo  → (integer) 1
```

## 4. Expiry

```text
> SET session x EX 5          → OK
> TTL session                 → (integer) 5
  ...wait 6 seconds...
> GET session                 → (nil)
```

## 5. Transactions and WATCH

```text
> MULTI                       → OK
> INCR a                      → QUEUED
> INCR a                      → QUEUED
> EXEC                        → 1) (integer) 1 2) (integer) 2
```

To check WATCH, run `WATCH a`, then `MULTI` and `INCR a`. Before you send `EXEC`,
run `SET a 100` from a **second** client. The `EXEC` then replies `(nil)`, which
means the transaction was aborted because the watched key changed.

## 6. Pub/Sub (two clients)

| Client A | Client B |
|---|---|
| `SUBSCRIBE news` | |
| | `PUBLISH news hello` → `(integer) 1` |
| prints `message news hello` | |

## 7. Lua scripting

```text
> EVAL "return redis.call('INCRBY', KEYS[1], ARGV[1])" 1 counter 5   → (integer) 5
> FUNCTION LOAD "#!lua name=lib\nredis.register_function('hi', function() return 'hi' end)"
> FCALL hi 0                  → "hi"
```

## 8. Persistence: snapshot

```text
> SET keep me                 → OK
> SAVE                        → OK        (writes /tmp/dc1/dump.dcs)
```

Stop the server with Ctrl-C and start it again with the same command. Then
`GET keep` should return `"me"`.

## 9. Persistence: AOF and crash recovery

```sh
./bin/distcache-server --port 6379 --dir /tmp/dc1 --appendonly yes --appendfsync always
```

```text
> SET durable 1               → OK
```

Kill the server without letting it shut down cleanly:
`kill -9 $(pgrep -f distcache-server)`. Restart it with the same command;
`GET durable` should return `"1"`. With `--appendfsync everysec`, up to about one
second of writes can be lost on a crash. That trade-off is the same as in Redis.

## 10. Replication

```sh
mkdir -p /tmp/dc2
./bin/distcache-server --port 6380 --dir /tmp/dc2     # third terminal
```

```text
redis-cli -p 6380 REPLICAOF 127.0.0.1 6379   → OK
redis-cli -p 6379 SET r 42                   → OK
redis-cli -p 6380 GET r                      → "42"
redis-cli -p 6380 SET r 1                    → (error) READONLY ...
redis-cli -p 6379 INFO replication           → connected_slaves:1
```

## 11. Load test

```sh
./bin/distcache-bench -p 6379 -n 200000 -c 50 -t set,get           # one request at a time
./bin/distcache-bench -p 6379 -n 1000000 -c 50 -P 16 -t set,get    # pipelined
```

Each test prints requests per second and the p50/p95/p99 latency.
`make bench` runs the full matrix and writes a report to `bench/results/`.
If `redis-server` is on your PATH, the report also includes Redis numbers for comparison.

## 12. Automated suites

```sh
make test        # unit and integration tests, with the race detector
make test-e2e    # end-to-end: restart, SIGKILL recovery, replication, go-redis client
make cover       # coverage report (internal/server is gated at 80%)
REDIS_SERVER_ADDR=127.0.0.1:6391 go test ./internal/server -run Redis84   # compare replies against a real Redis 8.4
```

## Known differences from Redis 8.4

The project ledger tracks the known differences as bugs B20–B40. Most are
differences in error-message wording. You may also notice these:

- `INFO server` reports `redis_version:7.4.0` instead of 8.4.
- `GEOPOS` formats coordinates with more digits than Redis.
- `BLMOVE` and `BRPOPLPUSH` reply with a null bulk string on timeout instead of a null array.
- `DEBUG` is always enabled. Do not expose the port to untrusted networks.
