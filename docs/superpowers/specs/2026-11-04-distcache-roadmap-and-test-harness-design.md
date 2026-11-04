# DistCache: Roadmap and Sub-project 1 Design (Test Harness + Baseline)

Date: 2026-11-04
Status: approved in discussion; awaiting written review

## 1. Purpose and constraints

DistCache is a Redis-compatible server in Go. The project serves four purposes at once, and every design decision must satisfy all of them:

1. **Learning and portfolio** — the code and docs must explain distributed-systems and high-performance server design clearly enough to teach from.
2. **Professional production use** — correctness, durability and operability are first-class.
3. **Drop-in Redis replacement** — RESP2/RESP3 wire compatibility with Redis 8.4 is a hard constraint. Any existing Redis client, in any language, must work unchanged. No custom wire protocol.
4. **Higher throughput than Redis on one machine** — Redis executes commands on a single thread; DistCache executes them in parallel on all cores. The goal is to actually realise that advantage, measured against a real `redis-server` on the same hardware.

**Compatibility target: Redis 8.4** (raised from 7.4 on 2026-11-04). The core command set and reply formats tracked are those of Redis Open Source 8.4. Redis 8 bundles former modules (JSON, Query Engine, time series, probabilistic types) and adds vector sets; these are listed as gaps (section 2.1) and scheduled individually rather than assumed. The exact 8.0→8.4 command delta must be verified against the 8.4 release notes and `COMMAND DOCS` of a real `redis-server` 8.4 at the start of each sub-project, since this spec was written from source reading, not the release notes.

**Platforms: Linux and macOS are first-class** — all throughput work is designed, tuned and benchmarked for them (Linux is the production reference). **Windows Server is deferred and optional** (decision 2026-11-04): CI only checks that the module compiles and vets for `GOOS=windows`; section 4.5 records the known Windows work for a later sub-project.

Explicitly out of scope: client-side caching (CLIENT TRACKING) — treated as a separate problem; a custom binary protocol — rejected because it would require bespoke SDKs in four languages and the measured bottleneck is not RESP parsing.

Horizontal scaling will follow the Redis Cluster *client* contract (16384 hash slots, hash tags, `MOVED`/`ASK`, `CLUSTER SHARDS/SLOTS/NODES`), so cluster-aware clients work unchanged. The Redis node-to-node bus protocol is undocumented, so DistCache nodes will use their own bus; interop with real Redis cluster *nodes* is not a goal.

## 2. Findings that drive the roadmap

### 2.1 Feature coverage (good)

247 commands are registered, covering every Redis 7.4 data type (Redis 8.4 delta still to verify) including hash-field TTL, functions, sharded pub/sub, RESP3, PSYNC2-style replication with backlog, WAIT/WAITAOF, keyspace notifications. Remaining gaps:

| Gap | Status today |
|---|---|
| CLUSTER family, ASKING, MIGRATE, FAILOVER | absent |
| ACL user model | stub: single `default` user backed by `requirepass` |
| Automatic AOF rewrite, multi-part AOF, `min-replicas-to-write` | absent |
| LATENCY, DEBUG, MODULE | canned replies |
| TLS | absent |
| Redis 8.x additions: vector sets (`VADD`, `VSIM`, …), JSON (`JSON.*`), Query Engine (`FT.*`), time series (`TS.*`), probabilistic (`BF.*`, `CF.*`, `CMS.*`, `TOPK.*`, `TDIGEST.*`), and any 8.2/8.4 core-command additions | absent except `HGETDEL/HGETEX/HSETEX`; exact 8.4 list to be verified against a real 8.4 server |
| Windows runtime behaviour | compiles (`GOOS=windows go vet` passes) but untested; see 4.5 |
| Tests | 3 Lua interpreter tests; zero server/storage/RESP tests; `internal/testutil` unused; no benchmarks, Makefile, CI, Dockerfile |

### 2.2 Throughput ceiling (the real problem)

The sharded-lock design is sound, but a code audit found serialization points that cap throughput well below the goal. Worst first:

1. `propagate()` takes a **global mutex on every write** before checking whether AOF/replicas are active, while shard locks are still held (`internal/server/propagate.go:65`).
2. **Shared atomic counters** updated by every core on every command (`cmd.Calls/Usec`, `stats.commands`, `ks.used`, `ks.sizes[db]`): cache-line ping-pong.
3. **~10 heap allocations per GET, ~15 per SET**: argv + per-arg buffers in the RESP reader, `Ctx`, `storage.DB`, `OnExpire` closure, `execUnit`, `keys []string`, `atomic.Value` boxing of `lastCmd`.
4. **Reads write shared memory**: `touch()` updates LRU/LFU atomics on every lookup even under `noeviction`.
5. 5–8 `time.Now()` calls and 4–5 key hashes per command.
6. EVAL/FCALL/EXEC take **all shard locks plus the snapshot barrier**, serializing the whole server.
7. WATCH and blocking-key registries fall back to one global mutex once any client uses them.
8. **Race bug**: `resp.Writer.maybeFlush` writes to the socket without the client's write mutex, so pub/sub pushes can interleave bytes (`internal/resp/writer.go:63`).
9. No `recover()` around handlers: one nil dereference kills the process.

Structural notes: everything lives in one `package server` on `*Server`; replication writes raw bytes to the conn; scripting bypasses `processCommand`; time and rand are not injectable (~25 raw `time.Now()` sites in handlers).

### 2.3 Redis reference points

- Redis command execution is single-threaded; `io-threads` only move bytes.
- Published redis-benchmark numbers: ~180k ops/s unpipelined, ~1.5–1.8M ops/s with `-P 16` (GET/SET, loopback, laptop).
- Reply batching: 16 KB static buffer per client, flushed from `beforeSleep`.
- Cluster client contract: `HASH_SLOT = CRC16-XMODEM(key) mod 16384`, hash tag is the substring between the first `{` and the next `}` if non-empty; `-MOVED <slot> <host:port>`; `ASKING` is a one-shot flag; migration via `CLUSTER SETSLOT ... IMPORTING/MIGRATING`, `GETKEYSINSLOT`, `MIGRATE`, `SETSLOT ... NODE`.

## 3. Roadmap: six sub-projects

Each sub-project gets its own spec → implementation plan → implementation cycle. This document is the full spec only for sub-project 1; sections 4–8 record the agreed direction for the others so later specs start from a shared understanding.

| # | Sub-project | Depends on | Outcome |
|---|---|---|---|
| 1 | Test harness + baseline (+ README rewrite) | — | Safety net, tooling, measured baseline |
| 2 | Single-node throughput (approach C) | 1 | Multi-core throughput that beats Redis on the same box, proven by load tests |
| 3 | Cluster mode | 2 | Redis-Cluster-client-compatible horizontal scaling |
| 4 | Production hardening | 1 | ACL users, auto AOF rewrite, min-replicas, FAILOVER, TLS |
| 5 | Client examples + browser gateway | 3 | Go, Java, Python, Node/TypeScript examples; HTTP/WebSocket gateway for React |
| 6 | Technical guides | each | One numbered guide per subsystem, written as each sub-project lands |

Order rationale: tests must exist before the hot path is refactored (1 → 2); cluster routing lives in the same hot path, so it follows the refactor (2 → 3); hardening is independent and can interleave; examples need cluster to demonstrate scaling; docs are written continuously, not at the end.

## 4. Sub-project 1 design: test harness + baseline

### 4.1 Goals

- Make the hot path safely refactorable: every command family and every subsystem has tests that fail when behaviour changes.
- Make correctness *measurable against Redis*: differential tests run the same command sequences against DistCache and a real `redis-server` and diff the rendered replies.
- Make performance *measurable*: an in-process microbenchmark (no network) and a network load tool that runs identically against DistCache and Redis, with results recorded in the repo.
- Standard tooling: Makefile, CI with the race detector, lint, Dockerfile.
- Fix the two defects tests will immediately expose: handler panics kill the process; unsynchronised socket write in `Writer.maybeFlush`.
- Rewrite README.md to describe what exists.

Non-goals for this sub-project: any performance change beyond the two bug fixes; any new command; cluster work.

### 4.2 Test pyramid and layout

| Layer | Location | Mechanism | Runs in CI |
|---|---|---|---|
| Unit | `internal/<pkg>/*_test.go` beside the code | Plain `go test`; fuzz targets for RESP reader and glob matcher | always, `-race` |
| Integration (command families) | `internal/server/*_test.go` | In-process server via `testutil.StartServer`, real TCP, table-driven command scripts | always, `-race` |
| Differential vs Redis | same files, `t.Skip` when `redis-server` is not on PATH or `DISTCACHE_SKIP_REDIS` is set | Same script run against both servers, `testutil.Render` output compared | always; CI job provides a `redis:8.4` service container |
| End-to-end | `test/e2e/*_test.go` (package `e2e`) | Multiple in-process servers (master + replica, restart with AOF/snapshot), real client library (`go-redis`) as a second opinion on wire compatibility | always, `-race` |
| Load | `cmd/distcache-bench` + `make bench` | Network load generator; results written to `bench/results/` | manual (`make bench`), nightly CI optional later |
| Microbenchmark | `internal/server/bench_test.go` | `Benchmark*` calling the in-process runner, `b.ReportAllocs()` | manual (`make microbench`) |

`go-redis` becomes the project's only third-party dependency, and only in `test/e2e` and examples (a separate `go.mod` under `test/e2e` keeps the server module dependency-free).

### 4.3 Components to build

#### A. In-process command runner (`internal/server`)

Exported API so tests (and future embedders) can execute commands without TCP:

```go
// Exec runs one command as an internal client and returns the parsed reply.
func (s *Server) Exec(args ...string) (resp.Value, error)

// Session is a stateful in-process client (SELECT, MULTI, WATCH, AUTH, RESP3 state).
type Session struct{ ... }
func (s *Server) NewSession() *Session
func (sess *Session) Exec(args ...string) (resp.Value, error)
```

Implementation: a `Client` with no `net.Conn`, `resp.NewWriter` over a `bytes.Buffer`, fed through `processCommand` (the same fake-client pattern the AOF loader already uses in `propagate.go`), reply parsed back with `resp.Reader`. Blocking commands with zero timeout are rejected in `Exec` with an error to avoid hanging tests; the TCP path is used for blocking tests.

#### B. Injectable clock

`Server` and `storage.Keyspace` get a `Clock` (`func() time.Time`), defaulting to `time.Now`. The ~25 handler sites that call `time.Now()` switch to `ctx.Now()` (which reads `DB.Now`, already set per call). `testutil.StartServer` accepts a mutate hook that can install a `testutil.FakeClock` with `Advance(d)`. This is the one refactor permitted in this sub-project because expiry, LRU/LFU and stream-ID tests are otherwise non-deterministic. Rand stays global; tests that depend on randomness assert properties, not values.

#### C. Unit tests

- `internal/resp`: reader (RESP2/RESP3 all types, inline commands, bulk limits, malformed input → `ProtocolError`), writer (every `Write*` method, RESP2 vs RESP3 encodings), fuzz target for the reader.
- `internal/storage`: shard selection and lock ordering (multi-key sets locked ascending), `DB` operations, lazy and active expiry with fake clock, LRU/LFU accounting, memory accounting, snapshot copy-on-write (`BeginSnapshot` + concurrent writes see consistent cut), codec round-trip for every value type, CRC validation.
- `internal/ds`: list, hashset, zset (skiplist ordering, rank, range by score/lex), stream (ID generation, trimming, consumer groups), HLL cardinality error bound, geo encode/decode, glob matcher (+ fuzz).
- `internal/config`: file parsing, CLI args, `include`, `save` semantics, immutable params, `CONFIG REWRITE` round-trip.
- `pkg/logger`: level mapping.

#### D. Integration tests per command family

One file per handler file (`cmd_string_test.go`, `cmd_list_test.go`, …). Each test is a table of `{cmd []string, want string}` steps rendered with `testutil.Render` (sorted where Redis order is unspecified). Every registered command is exercised at least once, including error paths (wrong arity, wrong type, syntax errors) so the exact Redis error strings are locked in. Cross-cutting tests: MULTI/EXEC/WATCH, pub/sub (incl. sharded and RESP3 push), blocking ops with timeouts and `CLIENT UNBLOCK`, scripting (EVAL, FUNCTION, script kill), keyspace notifications, MONITOR, SLOWLOG, CLIENT PAUSE, SHUTDOWN semantics, AUTH/HELLO.

A coverage gate: a test enumerates `s.cmds` and fails if any registered command name never appears in a test table.

#### E. Differential tests

`testutil.Differential(t, script)` starts both servers, runs the script on each, and reports the first differing step. Applied to the same tables as D by a flag, so every family is diffed against Redis for free. Known, documented divergences (e.g. `INFO` contents, `DUMP` payload format, `OBJECT ENCODING` names) are filtered by an explicit allowlist in the test — the allowlist is the authoritative list of intentional incompatibilities.

#### F. End-to-end tests (`test/e2e`)

- Persistence: write data → `SAVE`/`BGSAVE` → restart server in same dir → data present; same with AOF (`appendonly yes`, each `appendfsync` mode, `BGREWRITEAOF`).
- Replication: master + replica in-process; writes propagate; `WAIT` semantics; replica restart performs partial resync (check `INFO replication` offsets); chained replica.
- Client compatibility: `go-redis` against DistCache for pipelines, transactions, pub/sub, scripting, RESP3.
- Resilience: a handler that panics (injected via a test-only command registered under a build tag) is recovered, the client gets an error, the server keeps serving.

#### G. Load tool (`cmd/distcache-bench`)

A Go load generator with the redis-benchmark option subset `-h -p -c -n -P -d -t -r -q --csv`, so the same invocation runs against DistCache and Redis. Reports ops/s and p50/p95/p99 latency per command. `make bench` runs a fixed matrix (GET/SET/INCR/LPUSH/… at `-P 1` and `-P 16`, `-c 50` and `-c 500`) against DistCache and, if present, `redis-server`, and writes a markdown table to `bench/results/<date>-<hostname>.md`. Those numbers are the baseline sub-project 2 must beat.

#### H. Microbenchmarks

`internal/server/bench_test.go`: `BenchmarkGet`, `BenchmarkSet`, `BenchmarkPipeline` through the in-process runner, parallel variants with `b.RunParallel`, `b.ReportAllocs()`. These isolate execution cost from network cost and give the allocation counts sub-project 2 will drive down.

#### I. Tooling

- `Makefile`: `build`, `test` (unit+integration, `-race`), `test-e2e`, `test-all`, `lint` (golangci-lint), `bench`, `microbench`, `cover` (HTML report), `docker`.
- `.golangci.yml` with a conservative set (govet, staticcheck, errcheck, gosimple, ineffassign, unused).
- `.github/workflows/ci.yml`: matrix on Go 1.27, Linux + macOS + Windows; `redis:8.4` service on Linux for differential tests; race detector; coverage artifact.
- `Dockerfile`: multi-stage, distroless runtime, exposes 6379, `distcache.conf` optional.

#### J. Bug fixes

- `serveClient` and `call` recover from handler panics: log with stack, reply `-ERR internal error`, close that client only.
- All socket writes go through the client's write mutex (fix `Writer.maybeFlush` by giving the writer a flush callback that the client wraps with `wmu`).

#### K. README rewrite

Accurate description of what exists: features table, quick start, configuration (redis.conf style), compatibility statement with the divergence allowlist, how to run tests and benchmarks, project layout, roadmap pointing at this spec. Diagrams in Mermaid (open question: the user mentioned a tool "archify" for end-to-end flow diagrams; until clarified, Mermaid is used and diagrams are kept in a single place so they can be swapped).

### 4.5 Windows Server support (deferred — not part of sub-project 1)

Findings: the module builds for Windows today. Known runtime risks:
- `os.Rename` over a file that is still open fails on Windows; AOF rewrite (`propagate.go:273`) renames onto the live AOF and snapshot save (`persistence.go:122`) onto the dump file. Fix: close (or reopen with `FILE_SHARE_DELETE`-equivalent semantics) before rename, then reopen; covered by an e2e test run on Windows.
- Signals: `SIGTERM` is never delivered on Windows; Ctrl+C / Ctrl+Break arrive as `os.Interrupt`. Shutdown must hook `os.Interrupt` on all platforms (already does via `SIGINT`) and console-close events.
- Running as a **Windows service** needs the Service Control Manager API. Options: (a) add `golang.org/x/sys/windows/svc` — Go-team maintained, the only dependency, Windows-only build tag; (b) stay zero-dependency and document running under WinSW/NSSM. Default: (a), since `x/sys` is part of the Go project; decision recorded as open question until the user confirms.
- Unix sockets: supported by Go on Windows 10 1803+ / Server 2019+; keep the option, test it on Windows CI.
- File paths in config (`dir`, `dbfilename`) use `filepath` everywhere; tests use `t.TempDir()`.
- Differential tests skip on Windows (no official `redis-server` build); all other suites run.

Deferred deliverables (future sub-project): CI test job on `windows-latest`; `make`-free equivalents (`go test` commands documented, plus `scripts/test.ps1`); rename fix; `cmd/distcache-server` service mode behind `//go:build windows` with `--service install|uninstall|run`; Windows release zip in the Docker/CI pipeline.

### 4.4 Success criteria

- `make test-all` green with `-race` on Linux and macOS; `GOOS=windows go vet ./...` passes in CI.
- Every registered command appears in at least one integration test (enforced by the coverage gate).
- Differential suite passes against `redis-server` 8.4 with every divergence listed in the allowlist.
- Line coverage ≥ 80% for `internal/resp`, `internal/storage`, `internal/ds`, `internal/config`; ≥ 60% for `internal/server`.
- `bench/results/` contains a baseline for DistCache and Redis on the same machine.
- Handler panic test and writer-race test (run with `-race`) pass.

## 5. Sub-project 2 direction: single-node throughput (approach C)

Chosen over (A) hotspot fixes only and (B) shard-owned goroutines. B was rejected because its two cross-goroutine handoffs per command (~200–500 ns) cost more than the uncontended lock they replace on the dominant GET/SET path. C keeps "one goroutine executes a command start to finish" and removes contention instead:

1. Propagation gated by an `atomic.Bool`; no lock or allocation when AOF/replicas are off; lock-free MPSC buffer when on.
2. Per-shard (plain, under the shard lock) or padded per-core counters; `INFO` sums them.
3. Per-client reusable argv/arena, `Ctx`, `DB`, `execUnit`; `OnExpire` as a method value; `lastCmd` as `atomic.Pointer[Command]`.
4. One clock read per command (or per pipeline batch); coarse cached clock for LRU/LFU.
5. Shard index computed once per key and passed to storage.
6. `touch()` only when the eviction policy needs it, and only on change.
7. Lock-free read path: entries behind `atomic.Pointer`, readers never take the shard mutex; writers do.
8. Scripts and EXEC lock only the shards of declared keys; snapshot barrier becomes an RWMutex.
9. Sharded WATCH/blocker registries checked under the already-held shard lock.
10. Padded shard structs stored by value.
11. Reply writer batches pipelined replies into one `writev`; read timeout re-armed by cron, not per command.

Each step must move `make bench` or the microbenchmark, or it does not ship. Target: on the same machine, exceed `redis-server` throughput for the `-P 16 -c 50` matrix by a factor that scales with core count, and never regress unpipelined latency.

## 6. Sub-project 3 direction: cluster mode

- Routing: slot check in `preExecRejection` (both normal and MULTI-queue paths) and in the script `redis.call` path; `ASKING` flag on `Client`; `CROSSSLOT` for multi-key across slots; cluster mode forces DB 0.
- Storage: `shard = slot & (shards-1)` so a slot lives in exactly one shard; per-shard slot key counts; `COUNTKEYSINSLOT`/`GETKEYSINSLOT` scan one shard.
- Client-facing commands: `CLUSTER INFO/NODES/SLOTS/SHARDS/MYID/KEYSLOT/ADDSLOTS[RANGE]/DELSLOTS[RANGE]/SETSLOT/COUNTKEYSINSLOT/GETKEYSINSLOT/MEET/FORGET/REPLICATE/FAILOVER/RESET`, `ASKING`, `MIGRATE`, `READONLY/READWRITE`.
- Bus: own protocol (RESP-framed over TCP on port+10000), gossip PING/PONG/MEET/FAIL/UPDATE, config epochs, replica failover election following the Redis algorithm (delay = 500 ms + rand(500 ms) + rank × 1000 ms).
- Tests: cluster-aware clients (go-redis cluster, Jedis/Lettuce, redis-py cluster, ioredis cluster) must work unchanged; e2e for slot migration and failover.

## 7. Sub-project 4 direction: hardening

ACL users and rules (`ACL SETUSER/DELUSER/LOAD/SAVE`, categories, key/channel patterns, ACL LOG); automatic AOF rewrite (`auto-aof-rewrite-*`); `min-replicas-to-write/max-lag`; `FAILOVER`; TLS (`tls-port`, `tls-cert-file`…); real LATENCY tracking; graceful handling of client output buffer limits.

## 8. Sub-projects 5 and 6 direction: examples and guides

Examples use the standard client of each ecosystem (go-redis, Jedis or Lettuce, redis-py, ioredis), each a small runnable service showing caching, pub/sub, pipelining, transactions and cluster mode. The browser path is a thin Go gateway exposing HTTP + WebSocket (Server-Sent Events for pub/sub) consumed by a React example; this is the fastest practical browser interface because browsers cannot open raw TCP.

Guides live in `docs/guides/NN-<topic>.md`, one per subsystem (RESP, dispatch and locking, storage and eviction, persistence, replication, scripting, cluster, performance engineering, testing strategy). Each guide explains the concept, how Redis does it, how DistCache does it and why it differs, with diagrams. The guides are written for a reader learning the material; they do not reference their use for interview preparation.

## 9. Open questions

- "archify" for end-to-end flow diagrams: unknown tool; Mermaid assumed until clarified.
- Java client choice (Jedis vs Lettuce) — decide in sub-project 5.
- Which Redis 8 bundled capabilities (vector sets, JSON, Query Engine, time series, probabilistic) are in scope, and in which sub-project.

## 10. Decisions (2026-11-04)

- Windows: deferred and optional (superseded the earlier x/sys decision); Linux/macOS throughput is the priority. Server stays zero-dependency.
- Redis 8 bundled capabilities (vector sets, JSON, Query Engine, time series, probabilistic): deferred until after sub-project 3; each gets its own spec.
- Execution: subagent-driven, task by task with per-task review.
