# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

DistCache is a Redis-compatible (targets Redis 8.4 command semantics and replies) in-memory server written in Go 1.27 for Linux and macOS (Windows deferred), with **no external dependencies** (stdlib only — including its own Lua 5.1 interpreter). The main difference from Redis is that commands execute concurrently across a sharded keyspace instead of on a single thread.

Note: README.md is aspirational and out of date — the project structure, YAML config, Prometheus metrics and cluster/consistent-hashing sections do not reflect the code. Trust the code over the README.

## Commands

```sh
go build ./...                                   # build everything
go vet ./...
go test ./...                                    # run all tests
go test ./internal/lua -run TestLuaBasics -v     # single test
go run ./cmd/distcache-server --port 6380 --appendonly yes
go run ./cmd/distcache-server /path/to/distcache.conf --maxmemory 2gb
```

Layout follows Go convention (no `src/`): binaries in `cmd/`, all core code in `internal/`, end-to-end tests in `test/e2e` (own module), client examples in `examples/<lang>` (each its own project), React console in `web/console` (planned), Docker files in `deploy/docker`, scripts in `scripts/`. Configuration is redis.conf-style files and redis-server-style `--name value` args (plus legacy `-listenAddr host:port`), parsed in [internal/config/config.go](internal/config/config.go). Default port 6379, snapshot file `dump.dcs`, AOF `appendonly.aof`.

Integration tests should use [internal/testutil](internal/testutil/client.go): `StartServer(t, mutate)` boots an in-process server on a random port with a temp dir; `StartRedis(t)` starts a real `redis-server` (if on PATH, unless `DISTCACHE_SKIP_REDIS` is set) for side-by-side reply comparison via `Render`/`DoS`.

## Architecture

**Request path:** `serveClient` ([client.go](internal/server/client.go), one goroutine per connection) → RESP parsing ([internal/resp](internal/resp/)) → `processCommand` ([dispatch.go](internal/server/dispatch.go)) does Redis's pre-exec checks (arity, auth, MULTI queueing, pubsub context, LOADING/BUSY/READONLY/OOM/MISCONF via `preExecRejection`) → `call` acquires locks and runs the handler → `runHandler` does post-processing (propagation, WATCH invalidation, blocked-client wakeups, keyspace notifications).

**Command table:** every command is registered in [commands.go](internal/server/commands.go) as a `regSpec` (name, arity, flag string, first/last/step key positions, group, handler, optional `KeysFunc`). Handlers live in `cmd_<family>.go` and take a `*Ctx`. The flags drive behavior in the dispatcher, so getting them right matters:
- `write` / `readonly` decide write vs read shard locks and propagation; `denyoom` triggers eviction checks.
- Key positions (or a custom `KeysFunc`) determine **which shards get locked** — a wrong key spec means unlocked access to data.
- `global` = handler walks the keyspace and locks shards itself (KEYS, SCAN…); `lockall` = needs every shard lock and runs inside the snapshot barrier (FLUSHALL, SWAPDB, EVAL…).
- `txctl`, `noprop`, `no_multi`, `allow_busy`, `no_auth`, etc. mirror Redis flags.

**Sharded keyspace** ([internal/storage/keyspace.go](internal/storage/keyspace.go)): N shards (power of two, `--shards`, default 256), each with its own RWMutex and per-DB maps. Multi-key commands lock shards in ascending index order to avoid deadlock. All `storage.DB` methods assume the caller already holds the right shard locks. MULTI/EXEC and Lua scripts hold locks for their whole duration to stay atomic. Values are `[]byte` or the structures in [internal/ds](internal/ds/) (list, hash, set, zset, stream, plus HLL/geo/glob helpers). Memory is accounted per entry (`Entry.Mem`) for maxmemory/eviction (LRU/LFU/random/TTL policies).

**Handlers signal side effects through `Ctx`:** increment `ctx.dirty` when data changes (this is what triggers propagation, WATCH touch, wakeups, notifications); set `ctx.prop`/`propSet` to propagate something other than the literal args (e.g. relative expiry → absolute); set `ctx.blocked`/`blockKeys` for blocking commands — `call` then registers a waiter, releases locks, waits, and re-runs the handler.

**Propagation** ([propagate.go](internal/server/propagate.go)): write effects are serialized into a single ordered RESP stream feeding the AOF, the replication backlog and replicas. `flushUnit` must be called while still holding shard locks so per-key ordering is preserved. Lazily/actively expired keys propagate as `DEL`.

**Persistence** ([persistence.go](internal/server/persistence.go), [storage/codec.go](internal/storage/codec.go)): custom binary "DCSNAP" format (not RDB-compatible) used for SAVE/BGSAVE, AOF rewrite preamble, replica full sync and DUMP/RESTORE. Startup loads the AOF if appendonly is on, else the snapshot. BGSAVE is a copy-on-write point-in-time snapshot: while one is active, write handlers call `DB.Preserve(key)` before mutating (done automatically in `runHandler`), and `lockall` writes take the snapshot barrier.

**Replication** ([replication.go](internal/server/replication.go)): master→replica with PSYNC-style partial resync via backlog. Commands from the master connection (`c.isMaster`) bypass rejections, ignore expiry, and are not re-propagated.

**Scripting** ([scripting.go](internal/server/scripting.go), [internal/lua](internal/lua/)): EVAL/EVALSHA/SCRIPT and Redis 7 FUNCTIONs on an embedded, hand-written Lua 5.1 interpreter (lexer/parser/compiler/VM plus base, string, cjson-style libs). Scripts hold all shard locks (read locks for `_RO` variants) and replicate effects, not the script.

**Config** is live: hot values are atomics on `config.Config` read lock-free on the command path; CONFIG GET/SET/REWRITE go through the registered `param` table. Unsupported redis.conf directives are accepted and ignored via `ignoredParams`.
