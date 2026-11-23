# Compatibility with Redis

DistCache speaks the Redis protocol and aims to reply exactly as Redis 8.4
does. This page lists what is the same, what is different and what is
missing. It was measured on 2026-11-23 against Redis 8.4.8 (the `redis:8.4`
image) by asking both servers for their command tables, probing features one
by one, and running the comparison tests.

## Summary

| Area | Status |
|---|---|
| Core commands | 247 of Redis's 259 (95%). Every shared command reports the same arity as Redis. |
| Commands from the modules bundled with Redis 8 | 0 of 154: JSON, search, time series, probabilistic structures and vector sets are not implemented. |
| Replies | About 30 comparison scripts, covering every command family, return the same reply as Redis 8.4.8 step by step. 19 commands are exempt because their output describes the server itself (`INFO`, `CLIENT LIST`, `DUMP`, …). |
| Clients | Any Redis client should work. go-redis is exercised by the end-to-end tests. `examples/` has programs for go-redis, Jedis, redis-py and ioredis; only the Go one has been run so far. |
| Data files | Not compatible. DistCache cannot read `dump.rdb`, and Redis cannot read `dump.dcs`. |
| Replication with Redis | Not possible in either direction. DistCache replicates only with DistCache. |
| Cluster, Sentinel, ACL users, TLS on the Redis port | Not implemented. |

## Features

| Feature | Redis 8.4 | DistCache | Notes |
|---|---|---|---|
| Strings, lists, hashes, sets, sorted sets | yes | yes | |
| Hash field expiry (`HEXPIRE`, `HGETEX`, `HSETEX`, …) | yes | yes | |
| Streams and consumer groups | yes | yes | `XACKDEL` and `XDELEX` are missing |
| Bitmaps, HyperLogLog, geospatial | yes | yes | |
| JSON, search and vectors, time series, Bloom/cuckoo/top-k/t-digest, vector sets | yes | no | 154 commands |
| Key expiry, eviction (8 `maxmemory` policies) | yes | yes | |
| Transactions (`MULTI`, `EXEC`, `WATCH`) | yes | yes | |
| Lua scripts (`EVAL`) and functions (`FUNCTION`, `FCALL`) | yes | yes | Own Lua 5.1 interpreter with `cjson`, `cmsgpack` and `bit`; no `struct`; `SCRIPT DEBUG` is missing |
| Pub/Sub, pattern and sharded channels | yes | yes | |
| Keyspace notifications | yes | yes | |
| RESP2 and RESP3 (`HELLO`) | yes | yes | |
| Blocking commands (`BLPOP`, `BZPOPMIN`, `XREAD BLOCK`, …) | yes | yes | |
| Client-side caching (`CLIENT TRACKING`) | yes | no | Replies with an error |
| Snapshots | RDB | own format | `SAVE`, `BGSAVE`; file `dump.dcs` |
| Append-only file | yes | yes | Automatic rewrite is not implemented; run `BGREWRITEAOF` |
| `DUMP` / `RESTORE` | yes | yes | Payloads are not interchangeable with Redis |
| Replication | yes | yes | Primary to replicas, partial resync from the backlog; `WAIT` works |
| Automatic failover (Sentinel, `FAILOVER`) | yes | no | |
| Cluster mode | yes | no | `CLUSTER` replies "cluster support disabled", as a standalone Redis does |
| Password (`requirepass`, `AUTH`) | yes | yes | |
| ACL users and permissions | yes | no | Only the `default` user; `ACL SETUSER` replies with an error |
| Protected mode, protected configs, `enable-debug-command` | yes | yes | Same defaults as Redis |
| TLS | yes | no | Use a TLS-terminating proxy or a private network |
| Unix socket | yes | yes | |
| Modules (`MODULE LOAD`) | yes | no | |
| Command execution | one thread | all cores | Commands on different shards run in parallel |

## Commands Redis has and DistCache lacks

Core commands (12):

| Command | What it is |
|---|---|
| `MSETEX`, `DELEX`, `DIGEST` | Redis 8.4 string additions. The `SET` options `IFEQ`, `IFNE`, `IFDEQ` and `IFDNE` are missing too. |
| `XACKDEL`, `XDELEX` | Redis 8.2 stream additions |
| `FAILOVER` | Coordinated primary/replica switch |
| `MIGRATE` | Move keys to another server |
| `ASKING`, `RESTORE-ASKING`, `TRIMSLOTS` | Cluster only |
| `PFDEBUG`, `PFSELFTEST` | HyperLogLog internals |

Module commands (154):

| Family | Commands |
|---|---:|
| Search and query (`FT.*`) | 45 |
| JSON (`JSON.*`) | 25 |
| Time series (`TS.*`) | 19 |
| Cuckoo filter (`CF.*`) | 14 |
| t-digest (`TDIGEST.*`) | 14 |
| Vector sets (`VADD`, `VSIM`, …) | 13 |
| Bloom filter (`BF.*`) | 11 |
| Top-K (`TOPK.*`) | 7 |
| Count-min sketch (`CMS.*`) | 6 |

Subcommands that reply with an error:

| Command | Missing subcommands |
|---|---|
| `ACL` | `SETUSER`, `DELUSER`, `DRYRUN`, `GENPASS`, `LOAD`, `SAVE` (the read-only ones work) |
| `CLIENT` | `CACHING`, and `TRACKING ON` |
| `CLUSTER` | all, as in a standalone Redis |
| `SCRIPT` | `DEBUG` |
| `MODULE` | `LOAD`, `LOADEX` |

## Differences in commands that exist

- `COMMAND` reports different key positions for nine commands: `OBJECT`,
  `XGROUP`, `XINFO` (2,2,1 instead of 0,0,0), `ZUNIONSTORE`, `ZINTERSTORE`,
  `ZDIFFSTORE` and the sharded pub/sub commands (0,0,0 instead of 1,1,1).
  This affects only clients that route by `COMMAND` output; locking is correct.
- `COMMAND DOCS` returns a summary line and a link, without argument syntax.
- `INFO` has 12 of Redis's sections and 94 of its 240 fields. `Latencystats`,
  `Threads` and `Keysizes` are missing. It adds Go runtime fields
  (`goroutines`, `go_heap_alloc`, …) and `shards`.
- `CONFIG` knows 112 of Redis's 275 parameters. 65 of those are accepted so
  that an existing `redis.conf` loads, and then ignored; `CONFIG GET` returns
  an empty string for them. DistCache adds `shards`,
  `pubsub-max-queued-messages` and the `web-*` settings.
- `HELLO` and `INFO` report `redis_version:8.4.0`; the DistCache version is
  in `distcache_version`.
- `OBJECT ENCODING` reports Redis's encoding names, but DistCache does not
  use those encodings internally.
- `MEMORY USAGE` and `used_memory` are DistCache's own estimates.
- Some error messages are worded differently, for example when a script
  assigns to a global.
- A script cannot use an expression nested more than 16,384 levels deep.

## How to check it yourself

```sh
docker run -d --rm --name redis84 -p 6391:6379 redis:8.4
REDIS_SERVER_ADDR=127.0.0.1:6391 go test ./internal/... -run 'Differential|Redis84' -v
```

Each `…Differential` test runs one script, step by step, on DistCache and on
that Redis and fails on the first reply that differs.
