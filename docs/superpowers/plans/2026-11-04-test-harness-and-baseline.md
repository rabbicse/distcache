# Test Harness and Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give DistCache a complete test pyramid (unit, integration, differential-vs-Redis, e2e, load, microbench), standard tooling, two bug fixes, and an accurate README, so the hot path can be refactored safely in sub-project 2 against a measured baseline.

**Architecture:** Tests reuse the existing `internal/testutil` client and the AOF loader's fake-client pattern to run commands in-process; a new `Server.Exec`/`Session` API exposes that path. Time becomes injectable through one `Now func() time.Time` on `Server` and `Keyspace` so expiry tests are deterministic. A Go load generator with redis-benchmark's flag subset runs identically against DistCache and Redis.

**Tech Stack:** Go 1.27 stdlib only in the server module; `github.com/redis/go-redis/v9` only inside the separate `test/e2e` module; golangci-lint; GitHub Actions; Docker (distroless).

**Spec:** `docs/superpowers/specs/2026-11-04-distcache-roadmap-and-test-harness-design.md` (section 4)

## Global Constraints

- Server module (`github.com/rabbicse/distcache`) keeps **zero** `require` lines in `go.mod`.
- Go `1.27` (`toolchain go1.27.3`, already set in go.mod); all tests pass with `-race` on Linux and macOS; Windows is compile/vet-only in CI (deferred).
- Redis compatibility target is `8.4`; differential tests skip (not fail) when `redis-server` is absent or `DISTCACHE_SKIP_REDIS` is set.
- No performance changes in this sub-project beyond the two bug fixes (spec 4.1).
- Coverage floors: ≥ 80 % lines for `internal/resp`, `internal/storage`, `internal/ds`, `internal/config`; ≥ 60 % for `internal/server`.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Go naming and documentation (applies to every file touched):** identifiers follow Effective Go / Go Code Review Comments — MixedCaps, short receiver names, no stutter (`server.Server`, not `server.ServerStruct`), acronyms uppercased (`ID`, `AOF`, `TTL`, `RESP`), no single-letter names beyond loop indices and receivers, no abbreviations that are not idiomatic (`cfg`, `ctx`, `err`, `buf` are fine; `prop`, `repl`, `ks` are not — rename to `propagator`, `replication`, `keyspace` when a task modifies the declaring file). Every exported identifier has a doc comment starting with its name; every unexported type and non-trivial function has a comment saying *why* it exists; each file starts with a short comment on its responsibility. Comments are full sentences. Tests use `TestXxx_Scenario` naming and `t.Run` subtests for tables. `make lint` must pass.

## Review Focus

1. A pipelined client sending a command that panics in its handler must get `-ERR internal error` for that command, and the *next* pipelined command must still be answered. (Task 2 tests this.)
2. A RESP3 subscriber receiving a >64 KB message while the owning goroutine flushes a reply must receive bytes in order, never interleaved. (Task 3 tests under `-race`.)
3. `Server.Exec` with a blocking command and timeout 0 must return an error immediately rather than hang the test binary. (Task 4 tests this.)
4. A fake clock that jumps backwards must not make the active-expiry cron delete keys or panic. (Task 5 tests this.)
5. Differential tests must not silently pass because *both* servers returned an error: a step whose expected value is an error must match the Redis error prefix exactly. (Task 10 tests this.)

---

### Task 1: Tooling — Makefile, lint, CI, Dockerfile

**Files:**
- Create: `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `Dockerfile`, `.dockerignore`
- Modify: `.gitignore` (add `bin/`, `coverage.html`)

**Interfaces:**
- Produces: `make build test test-e2e test-all lint cover bench microbench docker`. Every later task's verification uses `make test`.

- [ ] **Step 1: Write `Makefile`** with targets:
  - `build`: `go build -o bin/distcache-server ./cmd/distcache-server && go build -o bin/distcache-bench ./cmd/distcache-bench` (bench binary exists from Task 15; until then `build` only builds the server — add the bench line in Task 15).
  - `test`: `go test -race -count=1 ./...`
  - `test-e2e`: `cd test/e2e && go test -race -count=1 ./...` (module created in Task 14; guard with `@test -d test/e2e || exit 0` until then).
  - `test-all`: `test test-e2e`
  - `lint`: `golangci-lint run ./...`
  - `cover`: `go test -count=1 -coverprofile=coverage.out ./... && go tool cover -html=coverage.out -o coverage.html`
  - `bench`, `microbench`: placeholders echoing "added in Task 15/16"; replaced there.
  - `docker`: `docker build -t distcache:dev .`
- [ ] **Step 2: Write `.golangci.yml`** enabling exactly: `govet, staticcheck, errcheck, gosimple, ineffassign, unused`; exclude `errcheck` for `_ =` patterns is default; set `run.timeout: 5m`.
- [ ] **Step 3: Write `.github/workflows/ci.yml`**: matrix `os: [ubuntu-latest, macos-latest, windows-latest]`, Go `1.27.x`; steps: checkout, setup-go, `make lint` (ubuntu only, via `golangci/golangci-lint-action`), `make test`, `make test-e2e`; on ubuntu add a `services: redis: image: redis:8.4` block and export `REDIS_SERVER_ADDR=127.0.0.1:6379` (Task 10 teaches `StartRedis` to use it); upload `coverage.out` artifact from `make cover`.
- [ ] **Step 4: Write `Dockerfile`**: stage 1 `golang:1.27` builds `/out/distcache-server` with `CGO_ENABLED=0`; stage 2 `gcr.io/distroless/static-debian12`, `COPY --from=build /out/distcache-server /distcache-server`, `EXPOSE 6379`, `VOLUME /data`, `ENTRYPOINT ["/distcache-server"]`, `CMD ["--dir","/data"]`. `.dockerignore`: `bin/ coverage.* .git docs`.
- [ ] **Step 5: Verify** — Run: `make build && make test && make lint`. Expected: all succeed (lint may list pre-existing findings; fix only trivial ones, otherwise add them to `.golangci.yml` `issues.exclude-rules` with a comment).
- [ ] **Step 6: Commit** — `git add Makefile .golangci.yml .github Dockerfile .dockerignore .gitignore && git commit -m "build: add Makefile, lint config, CI workflow and Dockerfile"`

---

### Task 2: Handler panic recovery

**Files:**
- Create: `internal/server/export_test.go`, `internal/server/recover_test.go`
- Modify: `internal/server/dispatch.go:200-299` (`call`), `internal/server/client.go:133-170` (`serveClient`)

**Interfaces:**
- Produces (test-only): `func (s *Server) RegisterTestCommand(name string, h func(*Ctx))` in `export_test.go` — appends a `regSpec{name, -1, "write", 1, 1, 1, "test", h, nil}` through the same path `registerCommands` uses.
- Produces: `const errInternal = "ERR internal error"` in `dispatch.go`.

- [ ] **Step 1: Write failing test** `TestHandlerPanicIsRecovered` in `recover_test.go` (package `server_test`): start via `testutil.StartServer`, register `"boom"` whose handler does `panic("kaboom")`, dial with `testutil.MustDial`, `Send("BOOM","k")` then `Send("PING")`, read two replies; assert first renders `-ERR internal error`, second `+PONG`; then `Do("SET","a","1")` on a *second* connection succeeds.
- [ ] **Step 2: Run** `go test ./internal/server -run TestHandlerPanicIsRecovered` — Expected: FAIL (process panics or test times out).
- [ ] **Step 3: Implement**: wrap `cmd.Handler(ctx)` inside `runHandler` with a deferred `recover()` that logs `s.log.Error("handler panic", "cmd", cmd.Name, "err", r, "stack", string(debug.Stack()))`, increments `s.stats.unexpectedErrors`, sets `ctx.errored = true`, and writes `errInternal` to `ctx.W`. Because locks are released by `call` normally, no lock cleanup is needed inside the recover. Also wrap the body of `serveClient`'s loop with a deferred recover that logs and returns (closes only that client) so panics outside handlers (e.g. in reply writing) cannot kill the process.
- [ ] **Step 4: Run** the test — Expected: PASS. Run `make test` — Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -am "fix(server): recover handler panics instead of crashing the process"`

---

### Task 3: Serialise all socket writes through the client write mutex

**Files:**
- Modify: `internal/resp/writer.go:21-66`, `internal/server/client.go:120-125,205-220`
- Create: `internal/resp/writer_race_test.go`

**Interfaces:**
- Produces: `Writer.Mu *sync.Mutex` — when non-nil, `Flush` holds it around the `Out.Write` call. `Client.flush()` no longer locks `wmu` itself (the writer does). The push goroutine keeps locking `c.wmu` for its direct `Out` writes.

- [ ] **Step 1: Write failing test** `TestWriterFlushHoldsMutex` (package `resp`): `var mu sync.Mutex`; `w := NewWriter(&recordingWriter{}, 2); w.Mu = &mu`; goroutine A loops 200× `w.WriteBulk(make([]byte, 70*1024))` (crosses `flushThreshold`); goroutine B loops 200× `mu.Lock(); out.Write([]byte("PUSH")); mu.Unlock()`; `recordingWriter.Write` asserts `mu.TryLock()==false` (i.e. the caller holds it) and records a data race otherwise.
- [ ] **Step 2: Run** `go test -race ./internal/resp -run TestWriterFlushHoldsMutex` — Expected: FAIL (assertion: write without lock).
- [ ] **Step 3: Implement**: add `Mu *sync.Mutex` to `Writer`; in `Flush`, if `w.Mu != nil` lock/unlock around the write; `maybeFlush` unchanged (it calls `Flush`). In `newClient` set `c.w.Mu = &c.wmu`; change `Client.flush` to `return c.w.Flush()`. Confirm the replica stream writer (`replication.go:115,135,307,350`) still locks `c.wmu` directly — it must.
- [ ] **Step 4: Run** `make test` — Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -am "fix(resp): hold client write mutex in Writer.Flush to stop interleaved pushes"`

---

### Task 4: In-process command runner (`Exec` / `Session`)

**Files:**
- Create: `internal/server/exec.go`, `internal/server/exec_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Server) Exec(args ...string) (resp.Value, error)     // fresh Session each call
  type Session struct { /* unexported: c *Client, buf bytes.Buffer, r *resp.Reader */ }
  func (s *Server) NewSession() *Session
  func (ss *Session) Exec(args ...string) (resp.Value, error)
  func (ss *Session) Close()
  var ErrWouldBlock = errors.New("exec: blocking command with zero timeout")
  ```
  `Session.Exec` builds `[][]byte` from args, calls `s.processCommand(ss.c, argv)`, flushes `ss.c.w` into `ss.buf`, parses one value with `ss.r.ReadValue()`. The session client is `&Client{s: s, proto: 2, authed: true, user: "default", name/libName/libVer/lastCmd stored as in `newClient`}` with `w = resp.NewWriter(&ss.buf, 2)`; it is **not** added to `s.clients`. Before dispatch, if the command has `FlagBlocking` and its timeout argument (last arg for BLPOP/BRPOP/BZPOP*/BLMOVE/BRPOPLPUSH, `timeout` after key count for BLMPOP/BZMPOP, `BLOCK 0` for XREAD/XREADGROUP) is `0`, return `ErrWouldBlock`.

- [ ] **Step 1: Write failing tests** in `exec_test.go` (package `server`): `TestExecSetGet` (`Exec("SET","k","v")` → `+OK`, `Exec("GET","k")` → `$v`), `TestSessionKeepsState` (`SELECT 3`, `SET`, then `Exec("DBSIZE")` on same session → `:1`, on `s.Exec` → `:0`), `TestExecUnknownCommand` returns a `-ERR unknown command` value with nil error, `TestExecBlockingZeroTimeout` (`Exec("BLPOP","q","0")` → `ErrWouldBlock`), `TestExecBlockingWithTimeout` (`Exec("BLPOP","q","0.05")` → `(nil-array)` within 1 s).
- [ ] **Step 2: Run** `go test ./internal/server -run 'TestExec|TestSession'` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement `exec.go`** as specified in Interfaces. Use `server.New(config.Default(), nil)` without `Start` in tests — confirm `processCommand` works pre-Start (it does; `loadData` is only in `Start`).
- [ ] **Step 4: Run** tests — Expected: PASS. `make test` — PASS.
- [ ] **Step 5: Commit** — `git commit -am "feat(server): add in-process Exec and Session API"`

---

### Task 5: Injectable clock

**Files:**
- Modify: `internal/storage/keyspace.go:138-160,178,287,334`, `internal/server/server.go:65-141`, `internal/server/dispatch.go:206,238,287,305`, `internal/server/command.go` (add `Ctx.NowMs`, `Ctx.NowTime`), every handler site listed by `grep -n 'time.Now()' internal/server/cmd_*.go internal/server/expire.go internal/server/scripting.go internal/server/pubsub.go`
- Modify: `internal/testutil/client.go:138-160`
- Create: `internal/testutil/clock.go`, `internal/server/clock_test.go`

**Interfaces:**
- Produces: `Keyspace.Now func() time.Time` (default `time.Now`, set in `storage.New`); `Server.Now func() time.Time` (default `time.Now`; `New` copies it into `ks.Now`; tests set `s.Now` *before* `Start`); `func (c *Ctx) NowMs() int64 { return c.DB.Now }`; `func (c *Ctx) NowTime() time.Time { return time.UnixMilli(c.DB.Now) }`.
- Produces: `testutil.FakeClock` with `func NewFakeClock(start time.Time) *FakeClock`, `func (f *FakeClock) Now() time.Time`, `func (f *FakeClock) Advance(d time.Duration)`, `func (f *FakeClock) Set(t time.Time)` (mutex-guarded).
- Produces: `testutil.Options{Mutate func(*config.Config); Clock func() time.Time}` and `func StartServerOpts(t testing.TB, o Options) (*server.Server, string)`; existing `StartServer(t, mutate)` delegates to it.

- [ ] **Step 1: Write failing tests** in `clock_test.go` (package `server_test`): `TestLazyExpiryUsesInjectedClock` (fake clock; `SET k v PX 100`; `Advance(50ms)` → GET `$v`; `Advance(60ms)` → GET `(nil)`; `TTL k` → `:-2`), `TestActiveExpiryUsesInjectedClock` (set `cfg.Hz=100`; `SET k v PX 100`; `Advance(200ms)`; poll `DBSIZE` ≤ 1 s until `:0`), `TestClockJumpBackwardIsSafe` (SET with PX 100, `Set(start-1h)`, sleep 50 ms, `GET` still `$v`, no panic).
- [ ] **Step 2: Run** `go test ./internal/server -run Clock` — Expected: FAIL (compile: `Options`/`Now` undefined).
- [ ] **Step 3: Implement**: add the fields and helpers; replace `time.Now()` in `dispatch.go` `call`/`newDB` with `s.Now()`; in `storage` use `ks.Now()` for LFU minutes (`:287`) and `NewEntry` (`:334`) and `lfuMinutes`; in `expire.go:42` use `s.Now()`; in handlers replace `time.Now().UnixMilli()` with `c.NowMs()` and `time.Now()` with `c.NowTime()` (streams use it for IDs; `XADD *` must therefore be deterministic under the fake clock — keep the sequence counter logic). `activeExpireCycle` compares `ExpireAt < now` only; a backwards clock just expires nothing. Update `testutil` with `Options`, `StartServerOpts`, `FakeClock`.
- [ ] **Step 4: Verify no raw calls remain**: Run `grep -rn 'time.Now()' internal/server internal/storage | grep -v _test | grep -v 'Now = time.Now'` — Expected: only `server.go` default assignment, `client.go` `created`/`lastInteraction` (wall-clock stats, allowed), and `cron` ticker code.
- [ ] **Step 5: Run** `make test` — Expected: PASS.
- [ ] **Step 6: Commit** — `git commit -am "refactor: inject clock into Server and Keyspace for deterministic expiry tests"`

---

### Task 6: `internal/resp` unit tests and fuzz target

**Files:**
- Create: `internal/resp/reader_test.go`, `internal/resp/writer_test.go`, `internal/resp/fuzz_test.go`

- [ ] **Step 1: Write tests** (package `resp`):
  - `TestReadCommandMultibulk` (`*2\r\n$3\r\nGET\r\n$1\r\nk\r\n` → `[GET k]`), `TestReadCommandInline` (`GET "a b" c\r\n` → `[GET, a b, c]`), `TestReadCommandEmptyLine` (`\r\n` → nil args, nil err), `TestReadCommandBulkTooLarge` (`MaxBulkLen=4`, `$5` → `*ProtocolError` with message containing `invalid bulk length`), `TestReadCommandMultibulkTooLarge` (`*2000000` → ProtocolError), `TestReadCommandNegativeLen`, `TestReadCommandTruncated` (`*2\r\n$3\r\nGE` → `io.ErrUnexpectedEOF`).
  - `TestReadValueAllTypes`: table over `+OK`, `-ERR x`, `:42`, `$3\r\nabc`, `$-1`, `*2…`, `*-1`, `_`, `#t`, `,1.5`, `%1…`, `~1…`, `>1…`, `=8\r\ntxt:abcd`, `(123`, `!5\r\nerror`; assert `Type`, `Str`, `Int`, `Float`, `Null`, `len(Elems)`.
  - `TestSplitArgs`: quotes, escapes `\x41`, `\n`, unbalanced → `ok=false`.
  - `TestWriterRESP2vsRESP3`: for `WriteNull`, `WriteNullArray`, `WriteMapLen`, `WriteSetLen`, `WritePushLen`, `WriteDouble`, `WriteBool`, `WriteVerbatim` assert exact bytes under proto 2 and 3 (RESP2: `$-1`, `*-1`, map→`*2n`, set→`*n`, push→`*n`, double→bulk string, bool→`:1/:0`, verbatim→bulk).
  - `TestFormatDouble`: `1`→`1`, `1.5`→`1.5`, `inf`→`inf`, `-inf`→`-inf`, `1e21`→`1e+21`, `0.1+0.2` → `0.30000000000000004`.
  - `TestAppendCommandStrings` round-trips through `ReadCommand`.
  - `FuzzReadValue` and `FuzzReadCommand`: seed with the valid cases; invariant: never panics, and if `err == nil` re-encoding the value with the writer and re-reading yields an equal `Value`.
- [ ] **Step 2: Run** `go test -race -cover ./internal/resp` — Expected: PASS, coverage ≥ 80 %. Run `go test -fuzz=FuzzReadValue -fuzztime=20s ./internal/resp` once locally — Expected: no crashers (fix any found; add the crasher as a regression case).
- [ ] **Step 3: Commit** — `git add internal/resp && git commit -m "test(resp): unit and fuzz tests for reader and writer"`

---

### Task 7: `internal/storage` unit tests

**Files:**
- Create: `internal/storage/keyspace_test.go`, `internal/storage/expire_test.go`, `internal/storage/snapshot_test.go`, `internal/storage/codec_test.go`

- [ ] **Step 1: Write tests** (package `storage`), using `ks := New(8, 2)` and a `db := &DB{KS: ks, Idx: 0, Now: now, Write: true}` helper that locks via `ks.Lock(ks.ShardSet(keys, nil), true)`:
  - `TestShardSetSortedAndDeduped`: keys mapping to the same shard appear once; result ascending.
  - `TestLockOrderingNoDeadlock`: 8 goroutines × 1000 iterations locking random 3-key sets with `Lock`/`Unlock`; completes under `-race` within 10 s.
  - `TestSetGetDeleteAccounting`: `Set` then `DBSize(0)==1`, `UsedMemory()>0`; `Delete` → 0 / memory back to 0.
  - `TestLazyExpiry`: `SetExpire(k, now+100)`; `Lookup` at `Now=now+101` returns nil and calls `OnExpire(0,"k")` once; `ExpiresCount(0)==0` afterwards.
  - `TestIgnoreExpiryReplicaMode`: same key with `IgnoreExpiry=true` → still found.
  - `TestExpireShardSampling`: 100 volatile keys all expired; `ExpireShard(i,0,20,now,hook)` returns `(expired=20, sampled=20)` for first call.
  - `TestLRUTouchAndIdle`: `ks.Clock.Store(100)`; `Lookup` sets `e.LRU()==100`; `NoTouch` leaves it.
  - `TestLFUIncrementsAndDecays`: `LFULogFactor=0` → counter increments to 255 cap; with `LFUDecayTime=1` and `Now` advanced 2 min `LFUCounter` decreases by 2.
  - `TestEvictKeyRemovesAndAccounts`.
  - `TestFlushDBAndSwapDB`.
  - `TestSnapshotIsPointInTime`: `BeginSnapshot(cut)` where `cut` records `ForEachInShard` keys; concurrently `Set` 100 new keys and overwrite 10 old ones during the snapshot read phase (use `Preserve` as `runHandler` does); the snapshot view (via the overlay accessor used by `Encoder`) contains exactly the original keys with original values; after `EndSnapshot` the live data has the new writes.
  - `TestCodecRoundTripAllTypes`: encode string/list/set/zset/hash/stream (with groups/PEL) + expiry + aux into a buffer; `DecodeWithAux` yields deep-equal values and the same `exp`.
  - `TestCodecRejectsBadCRC` and `TestCodecRejectsBadMagic`.
- [ ] **Step 2: Run** `go test -race -cover ./internal/storage` — Expected: PASS, coverage ≥ 80 %.
- [ ] **Step 3: Commit** — `git add internal/storage && git commit -m "test(storage): keyspace, expiry, snapshot and codec unit tests"`

---

### Task 8: `internal/ds` unit tests

**Files:**
- Create: `internal/ds/list_test.go`, `internal/ds/hashset_test.go`, `internal/ds/zset_test.go`, `internal/ds/stream_test.go`, `internal/ds/hll_test.go`, `internal/ds/geo_test.go`, `internal/ds/glob_test.go`

- [ ] **Step 1: Write tests** (package `ds`), one file per structure, driven by the exported API in each file (read it first; names below are the constructors confirmed to exist):
  - List (`NewList`): push/pop both ends, index negative, range bounds, trim, rem count ±, insert before/after, pos with RANK/MAXLEN, `Bytes()` grows/shrinks, `Clone` is deep.
  - Hash/Set (`NewHash`, `NewSet`): set/get/del, random fields with/without count (negative count allows dups), intset-style integer members ordering if present, `Clone` deep.
  - ZSet (`NewZSet`): insert 1000 random scores, verify rank == sorted index for every member; range by score with exclusive bounds `(1`; range by lex with `[`/`(`/`-`/`+`; remove-range variants; score ties ordered by member lexicographically; `Clone` deep.
  - Stream (`NewStream`): `*` ID monotonic with fixed `ms`, explicit ID must be greater than last, `MAXLEN`/`MINID` trimming exact and `~`, consumer group PEL add/ack/claim, `XSETID` lowering rejected.
  - HLL: add 100 000 distinct values → count within 2 % of 100 000; merge of two disjoint sets ≈ union; sparse→dense promotion keeps count.
  - Geo: encode/decode 52-bit geohash round-trip within 1 m for 10 cities; distance Paris–Berlin ≈ 878 km ± 1 %.
  - Glob (`Match`): table: `h?llo`, `h*llo`, `h[ae]llo`, `h[^e]llo`, `h[a-b]llo`, escapes `\*`, nocase, unterminated `[`; plus `FuzzMatch` (never panics, matches within 1 ms for 64-byte inputs).
- [ ] **Step 2: Run** `go test -race -cover ./internal/ds` — Expected: PASS, coverage ≥ 80 %.
- [ ] **Step 3: Commit** — `git add internal/ds && git commit -m "test(ds): unit tests for list, hash, set, zset, stream, hll, geo and glob"`

---

### Task 9: `internal/config` and `pkg/logger` unit tests

**Files:**
- Create: `internal/config/config_test.go`, `pkg/logger/logger_test.go`

- [ ] **Step 1: Write tests** (package `config`): `TestParseArgsPairs` (`--port 6380 --appendonly yes --save 60 1 300 10` → fields), `TestParseArgsConfigFileThenOverride` (temp file `port 7000` + `--port 7001` → 7001), `TestLoadFileInclude`, `TestSaveEmptyStringDisables` (`save ""` → `Save==nil`), `TestSaveAccumulates` (two `save` lines → two rules), `TestImmutableParamRejectedAtRuntime` (`Set("port","1",false)` → error containing `can't set immutable config`), `TestParseMemory` (`1gb`, `100mb`, `1k`, `12`, `-1` error), `TestNotifyFlagsRoundTrip` (`KEA` → `NotifyFlagsString == "AKE"`), `TestRewriteRoundTrip` (set values, `Rewrite()`, `LoadFile` into fresh `Default()` → equal `Get("*")`), `TestLegacyListenAddr` (`-listenAddr 127.0.0.1:6400`), `TestIgnoredParamsAccepted` (`protected-mode yes` no error).
  Logger: `TestLevelMapping` for `debug/verbose/notice/warning/nothing` (assert `Enabled` at the expected `slog.Level`), `TestLogFileCreated`.
- [ ] **Step 2: Run** `go test -race -cover ./internal/config ./pkg/logger` — Expected: PASS, config ≥ 80 %.
- [ ] **Step 3: Commit** — `git commit -m "test(config,logger): unit tests"`

---

### Task 10: Integration harness — scripts, differential runner, coverage gate

**Files:**
- Create: `internal/testutil/script.go`, `internal/testutil/differential.go`, `internal/server/harness_test.go`, `internal/server/coverage_gate_test.go`
- Modify: `internal/testutil/client.go:163` (`StartRedis` honours `REDIS_SERVER_ADDR`: if set, return it with `ok=true` after a `PING`, and `FLUSHALL` it in `t.Cleanup`), `internal/server/export_test.go` (add `func (s *Server) CommandNames() []string`)

**Interfaces:**
- Produces:
  ```go
  type Step struct { Cmd []string; Want string; Sorted bool }   // Want is testutil.Render output
  func S(want string, cmd ...string) Step                          // helper
  func RunScript(t testing.TB, c *Client, steps []Step)            // fails at first mismatch: "step %d %v: got %s want %s"
  type Divergence struct { Cmd string; Reason string }             // allowlist entry: command name (lowercase)
  func Differential(t testing.TB, steps []Step, allow []Divergence) // starts DistCache + Redis, runs steps on both, compares Render; skips if Redis absent; steps whose command is in allow are executed but not compared
  var KnownDivergences []Divergence                                 // the authoritative list: info, dump, restore, object (encoding names), debug, memory, latency, lolwut, module, command (docs text), client (list formatting), config (param set), slowlog (ids), role, replconf, psync, sync, acl, hello (server name/version)
  func RecordCommands(t testing.TB) func(name string)              // used by the coverage gate (see below)
  ```
  Each `RunScript`/`Differential` call records lowercase command names into a package-level `sync.Map` in `testutil`; `func CoveredCommands() []string` returns them.
- [ ] **Step 1: Write failing tests** in `harness_test.go` (package `server_test`): `TestRunScriptReportsFirstMismatch` (use a `testing.TB` recorder; a wrong `Want` yields the formatted message), `TestDifferentialDetectsBothErrors` (steps `S("-ERR wrong number of arguments for 'get' command", "GET")`; against a stub where Redis says `-ERR wrong number of arguments for 'get' command` and DistCache says `-ERR something else` → fail; identical → pass) — implement with the real servers: DistCache's `GET` with no args must render exactly `-ERR wrong number of arguments for 'get' command`.
  In `coverage_gate_test.go`: `TestEveryCommandIsExercised` runs **last** (name it `TestZZCoverageGate`, and run it only when `-run` is empty, detected via `flag.Lookup("test.run").Value.String()==""`): compares `s.CommandNames()` with `testutil.CoveredCommands()` and fails listing uncovered names, excluding `{"sync","psync","replconf","monitor","shutdown","debug","module"}` which are covered by e2e/Task 13 directly.
- [ ] **Step 2: Run** `go test ./internal/server -run 'TestRunScript|TestDifferential'` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the files in Interfaces. `Differential`: `StartServer` + `StartRedis`; one `Client` each; for each step run on both, `Render(v, step.Sorted)`; compare; errors compare the full string.
- [ ] **Step 4: Run** `make test` — Expected: PASS except `TestZZCoverageGate` which FAILS listing ~240 uncovered commands (expected until Tasks 11–13). Temporarily mark it `t.Skip("enabled in Task 13")` and commit.
- [ ] **Step 5: Commit** — `git commit -am "test: integration script runner, differential-vs-Redis runner and command coverage gate"`

---

### Task 11: Integration tests — strings, bitmaps, keyspace, connection, server

**Files:**
- Create: `internal/server/cmd_string_test.go`, `cmd_bitmap_test.go`, `cmd_keys_test.go`, `cmd_connection_test.go`, `cmd_server_test.go`

- [ ] **Step 1: Write tests** (package `server_test`). Each file: `var stringScript = []testutil.Step{...}` covering **every** command of the family listed in the spec's inventory, including: happy path; wrong arity (`-ERR wrong number of arguments for '<cmd>' command`); WRONGTYPE; syntax error; options (`SET NX/XX/GET/EX/PX/EXAT/PXAT/KEEPTTL`, `GETEX`, `SET` with `GET` on wrong type, `INCR` overflow `-ERR increment or decrement would overflow`, `INCRBYFLOAT` NaN/inf, `LCS` with `LEN/IDX/MINMATCHLEN/WITHMATCHLEN`, `BITFIELD` overflow modes `WRAP/SAT/FAIL`, `BITOP NOT` arity, `BITPOS` bit ranges with `BIT/BYTE`, `OBJECT ENCODING` (allowlisted), `EXPIRE NX/XX/GT/LT`, `COPY DB/REPLACE`, `RENAME` same key, `SORT BY/GET/LIMIT/ALPHA/STORE`, `SCAN MATCH/COUNT/TYPE` (assert cursor terminates and union equals `KEYS *`), `RANDOMKEY` on empty db `(nil)`, `SWAPDB`, `MOVE`, `DUMP/RESTORE` round-trip (`DUMP` allowlisted, but `RESTORE` then `GET` compared), `SELECT 16` error, `PING` with/without message, `ECHO`, `HELLO 3` map keys (allowlisted) and `HELLO 2`/`HELLO 4` error, `AUTH` without password `-ERR AUTH <password> called without any password configured…`, `CLIENT ID/GETNAME/SETNAME/INFO/LIST/KILL/PAUSE/UNPAUSE/REPLY/NO-EVICT/NO-TOUCH/UNBLOCK/HELP`, `CONFIG GET/SET/RESETSTAT/REWRITE` (no config file → error), `COMMAND COUNT/INFO get/DOCS/LIST/GETKEYS`, `INFO` sections exist (allowlisted content: assert section headers only), `TIME`, `LASTSAVE`, `SAVE`, `BGSAVE` (then poll `INFO persistence` `rdb_bgsave_in_progress:0`), `DBSIZE`, `FLUSHDB/FLUSHALL SYNC/ASYNC`, `MEMORY USAGE/DOCTOR/STATS/HELP`, `LATENCY`, `LOLWUT`, `SLOWLOG GET/LEN/RESET` (set `slowlog-log-slower-than 0` → entries appear), `WAIT 0 0` → `:0`, `WAITAOF 0 0 0`, `ROLE` master, `ACL WHOAMI/USERS/CAT/LIST/LOG`, `RESET`, `QUIT`.
  Each file has `TestStringCommands` running `RunScript` and `TestStringCommandsDifferential` running `Differential(t, stringScript, testutil.KnownDivergences)`.
- [ ] **Step 2: Run** `go test -race ./internal/server -run 'String|Bitmap|Keys|Connection|ServerCmd'` — Expected: PASS. If the differential run reveals a DistCache deviation, **fix DistCache** (it is a bug) unless it is a documented divergence, in which case add it to `KnownDivergences` with a reason.
- [ ] **Step 3: Commit** — `git commit -am "test(server): integration + differential tests for string, bitmap, keyspace, connection and server commands"`

---

### Task 12: Integration tests — lists, sets, sorted sets, hashes, streams, geo, HLL

**Files:**
- Create: `internal/server/cmd_list_test.go`, `cmd_set_test.go`, `cmd_zset_test.go`, `cmd_hash_test.go`, `cmd_stream_test.go`, `cmd_hll_geo_test.go`

- [ ] **Step 1: Write tests** as in Task 11 for every command in these groups. Required specifics: blocking commands via a second `testutil.Client` (`BLPOP` wakes on `LPUSH`, timeout `0.1` returns `(nil-array)`, `BLMOVE`/`BLMPOP`/`BZMPOP`/`BZPOPMIN` wake order FIFO across two blocked clients, `CLIENT UNBLOCK <id> ERROR` → `-UNBLOCKED`); `LPOS RANK/COUNT/MAXLEN`; `LMPOP LEFT/RIGHT COUNT`; `SRANDMEMBER` negative count length; `SPOP count`; `SINTERCARD LIMIT`; `SMOVE`; `ZADD NX/XX/GT/LT/CH/INCR` combos incl. `-ERR XX and NX options at the same time are not compatible`; `ZRANGE BYSCORE/BYLEX/REV/LIMIT/WITHSCORES`; `ZRANGESTORE`; `ZUNION/ZINTER/ZDIFF` with `WEIGHTS/AGGREGATE/WITHSCORES`; `ZINTERCARD`; `ZRANDMEMBER WITHSCORES`; `ZMPOP`; `ZADD` NaN error; hash `HRANDFIELD WITHVALUES`, `HINCRBYFLOAT`, `HSCAN`, field TTL (`HEXPIRE k 1 FIELDS 1 f` → `*[:1]`, fake clock advance, `HGET` nil, `HTTL` → `-2`, `HPERSIST`, `HEXPIRE` with `NX/XX/GT/LT`, `HEXPIRE` on missing field → `-2`), `HGETDEL/HGETEX/HSETEX` compared (Redis 8.4 target); streams: `XADD` explicit/auto IDs, `NOMKSTREAM`, `MAXLEN ~`, `MINID`, `LIMIT`, `XRANGE -/+` with exclusive `(`, `XREVRANGE`, `XREAD COUNT/BLOCK` with a second client, `XGROUP CREATE MKSTREAM/ENTRIESREAD`, `XREADGROUP >` and `0`, `XACK`, `XPENDING` summary and extended with `IDLE`, `XCLAIM`/`XAUTOCLAIM JUSTID`, `XINFO STREAM FULL`, `XINFO GROUPS/CONSUMERS` (`Sorted`), `XDEL`, `XTRIM`, `XSETID` with `ENTRIESADDED/MAXDELETEDID`, `XLEN`; geo: `GEOADD NX/XX/CH`, `GEOPOS` precision (compare rounded to 4 decimals via `Sorted` false and an allowlist reason if Redis differs beyond 1e-6), `GEODIST` units, `GEOHASH`, `GEOSEARCH FROMMEMBER/FROMLONLAT BYRADIUS/BYBOX ASC/COUNT ANY/WITHDIST/WITHCOORD`, `GEOSEARCHSTORE STOREDIST`, `GEORADIUS` deprecated forms; HLL: `PFADD` returns 1 then 0, `PFCOUNT` multi-key, `PFMERGE`, `PFCOUNT` on wrong type/invalid HLL `-WRONGTYPE`/`-INVALIDOBJ`.
- [ ] **Step 2: Run** `go test -race ./internal/server -run 'List|Set|ZSet|Hash|Stream|HllGeo'` — Expected: PASS; fix DistCache deviations found by the differential run.
- [ ] **Step 3: Commit** — `git commit -am "test(server): integration + differential tests for collection, stream, geo and HLL commands"`

---

### Task 13: Integration tests — transactions, pub/sub, scripting, notifications, monitor, persistence commands; enable the coverage gate

**Files:**
- Create: `internal/server/cmd_tx_test.go`, `pubsub_test.go`, `scripting_test.go`, `notify_test.go`, `monitor_test.go`, `persistence_cmd_test.go`
- Modify: `internal/server/coverage_gate_test.go` (remove the `t.Skip`)

- [ ] **Step 1: Write tests**: MULTI/EXEC/DISCARD; `EXEC` without `MULTI`; queued error → `-EXECABORT`; runtime error inside EXEC returns per-command error; `WATCH` then modification from another client → `EXEC` `(nil-array)`; `WATCH` inside MULTI error; `UNWATCH`; nested `MULTI` error. Pub/sub: `SUBSCRIBE` confirmation shape, `PUBLISH` delivery to subscriber, `PSUBSCRIBE` pattern, `UNSUBSCRIBE` all, RESP2 context restriction error (`-ERR Can't execute 'get'…`), RESP3 push messages (`>`), `SSUBSCRIBE/SPUBLISH`, `PUBSUB CHANNELS/NUMSUB/NUMPAT/SHARDCHANNELS/SHARDNUMSUB`. Scripting: `EVAL` returning each Lua type → RESP mapping, `KEYS/ARGV`, `redis.call` error → `-ERR …`, `redis.pcall` returns error table, `EVALSHA` `-NOSCRIPT`, `SCRIPT LOAD/EXISTS/FLUSH`, `EVAL_RO` writing → `-ERR Write commands are not allowed from read-only scripts`, `SCRIPT KILL` of a busy loop from a second client with `lua-time-limit 100` → `-BUSY` then killed, `FUNCTION LOAD` with `#!lua name=lib`, `FCALL`, `FCALL_RO`, `FUNCTION LIST/DELETE/FLUSH/DUMP/RESTORE/STATS`, `cjson.encode/decode`, `redis.sha1hex`, `redis.setresp(3)`. Notifications: `CONFIG SET notify-keyspace-events KEA`, subscriber on `__keyevent@0__:set` receives the key; `expired` event with fake clock. `MONITOR`: second client sees `"SET" "k" "v"` formatted line. Persistence commands: `BGREWRITEAOF` with `appendonly yes` → `+Background append only file rewriting started`, poll `INFO persistence aof_rewrite_in_progress:0`; `CONFIG SET appendonly yes` creates the file in `Dir`; `SHUTDOWN` on a dedicated server → connection closes and `Start` returns nil; `DEBUG SLEEP 0`, `DEBUG RELOAD` keeps data, `MODULE LIST` → `*[]`, `SYNC`/`PSYNC ? -1` → reply starts with `+FULLRESYNC`, `REPLCONF listening-port 1` → `+OK` (these cover the gate's exclusions where feasible; keep the exclusion list to commands that genuinely cannot run in-process).
- [ ] **Step 2: Enable the gate** (remove `t.Skip`). Run `make test` — Expected: PASS including `TestZZCoverageGate`. If it lists uncovered commands, add steps for them.
- [ ] **Step 3: Verify coverage**: Run `go test -count=1 -coverprofile=/tmp/c.out ./internal/server && go tool cover -func=/tmp/c.out | tail -1` — Expected: total ≥ 60 %.
- [ ] **Step 4: Commit** — `git commit -am "test(server): tx, pub/sub, scripting, notification and persistence tests; enable command coverage gate"`

---

### Task 14: End-to-end tests with go-redis (`test/e2e` module)

**Files:**
- Create: `test/e2e/go.mod` (module `github.com/rabbicse/distcache/test/e2e`, `go 1.27`, `require github.com/redis/go-redis/v9` (latest v9 at implementation time), `replace github.com/rabbicse/distcache => ../..`), `test/e2e/helpers_test.go`, `test/e2e/persistence_test.go`, `test/e2e/replication_test.go`, `test/e2e/compat_test.go`, `test/e2e/resilience_test.go`
- Modify: `Makefile` (`test-e2e` real target)

**Interfaces:**
- Consumes: `testutil.StartServerOpts`, `server.Server.Shutdown(save bool)`. `Server.RegisterTestCommand` is **not** available outside package tests, so the resilience test uses `DEBUG PANIC`, which Redis itself provides: add it in **this task** to the `DEBUG` switch in `cmd_server.go` as `panic("DEBUG PANIC")`.
- Produces: `func startNode(t, dir string, extra func(*config.Config)) (*server.Server, *redis.Client)` helper.

- [ ] **Step 1: Write tests** (package `e2e`):
  - `TestSnapshotSurvivesRestart`: write 1000 keys of mixed types, `SAVE`, `Shutdown(false)`, start new server on same `dir`, assert all present with same values/TTLs (TTL within ±1 s).
  - `TestAOFSurvivesRestart` for `appendfsync` in `always/everysec/no`; and after `BGREWRITEAOF`; and with a truncated tail (append 10 garbage bytes to the file → server starts, logs warning, data intact).
  - `TestReplicationPropagates`: master + replica (`ReplicaOf` set via `REPLICAOF host port` command); write on master; poll replica `GET` ≤ 2 s; `WAIT 1 1000` → `1`; replica write → `-READONLY`; `INFO replication` on replica shows `master_link_status:up`.
  - `TestPartialResyncAfterReplicaRestart`: stop replica, write 100 keys on master, start replica again → `INFO stats` on master `sync_partial_ok:1`.
  - `TestChainedReplica`: A→B→C; write on A visible on C.
  - `TestGoRedisCompat`: pipeline of 1000 SET/GET, `TxPipeline`, `Watch` optimistic lock retry, `Subscribe` + `Publish`, `Eval`, `HSET/HGETALL` map decoding, RESP3 (`Protocol: 3`) `HGETALL` and `ZRANGE WITHSCORES` typed results, `Scan` iterator.
  - `TestCrashRecoveryEverysec`: build `bin/distcache-server`, run it as a **subprocess** with `--appendonly yes --appendfsync everysec --dir <tmp>`; 8 clients `INCR`/`SET k:<i> <i>` in a loop, recording each acknowledged write with its ack time; after 3 s send `SIGKILL`; restart on the same dir; assert every write acknowledged more than 1.5 s before the kill is present (spec section 11, level b) and the server starts cleanly (truncated tail repaired). Same test with `appendfsync always`: assert **every** acknowledged write is present. Skip on Windows.
  - `TestPanicDoesNotKillServer`: `DEBUG PANIC` → error reply; new connection `PING` works.
- [ ] **Step 2: Run** `make test-e2e` — Expected: PASS (go-redis is downloaded on first run; CI has network).
- [ ] **Step 3: Verify root module unchanged**: `cd $(git rev-parse --show-toplevel) && go mod tidy && git diff --exit-code go.mod` — Expected: no diff.
- [ ] **Step 4: Commit** — `git add test/e2e Makefile internal/server/cmd_server.go && git commit -m "test(e2e): persistence, replication, go-redis compatibility and resilience tests"`

---

### Task 15: Load tool `cmd/distcache-bench` and `make bench`

**Files:**
- Create: `cmd/distcache-bench/main.go`, `cmd/distcache-bench/runner.go`, `cmd/distcache-bench/runner_test.go`, `bench/run.sh`, `bench/results/README.md`
- Modify: `Makefile` (`build` adds the bench binary; `bench` target runs `bench/run.sh`)

**Interfaces:**
- Produces CLI: `distcache-bench [-h host] [-p port] [-c clients=50] [-n requests=100000] [-P pipeline=1] [-d datasize=3] [-t tests=set,get,incr,lpush,rpush,lpop,rpop,sadd,hset,zadd] [-r keyspace=0] [-q] [--csv] [--duration 0s]`.
- Produces: `type Result struct { Test string; Requests int; Seconds float64; OpsPerSec float64; P50, P95, P99 time.Duration }` and `func Run(cfg Config) ([]Result, error)` in `runner.go` using `resp.AppendCommandStrings` and `resp.Reader` directly (no third-party client), one goroutine per client, `-P` commands per write, latency sampled per pipeline batch into a sorted slice (reservoir of 100 000) for percentiles.
- Output: human table by default; `--csv` emits `test,ops_per_sec,p50_us,p95_us,p99_us`.

- [ ] **Step 1: Write failing test** `TestRunAgainstInProcessServer` (package `main`): `testutil.StartServer`, `Run(Config{Addr: addr, Clients: 4, Requests: 2000, Pipeline: 8, Tests: []string{"set","get"}})` → two results with `Requests==2000` and `OpsPerSec>0`; `-r 1000` produces keys `key:<n>` with `n<1000` (assert via `DBSIZE ≤ 1000`).
- [ ] **Step 2: Run** `go test ./cmd/distcache-bench` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `runner.go` and `main.go` (flag parsing mirrors redis-benchmark names exactly so the same script drives both binaries).
- [ ] **Step 4: Write `bench/run.sh`**: builds `bin/distcache-server` and `bin/distcache-bench`; starts DistCache on port 6390 (`--save "" --appendonly no --dir $TMP`); runs the matrix `{-P 1, -P 16} × {-c 50, -c 500}` with `-n 1000000 -t set,get,incr,lpush,sadd,hset,zadd -r 100000 --csv`; if `redis-server` is on PATH, starts it on 6391 with the same flags and runs the identical matrix; writes `bench/results/$(date +%F)-$(hostname -s).md` with a markdown table per configuration (DistCache vs Redis columns, ratio column) plus `go version`, `uname -a`, `nproc`/`sysctl -n hw.ncpu`; kills both servers on exit (`trap`).
- [ ] **Step 5: Run** `make bench` — Expected: results file written; commit it as the baseline (this file is the number sub-project 2 must beat). `bench/results/README.md` explains the matrix and how to reproduce.
- [ ] **Step 6: Commit** — `git add cmd/distcache-bench bench Makefile && git commit -m "bench: add distcache-bench load tool and baseline results"`

---

### Task 16: Microbenchmarks

**Files:**
- Create: `internal/server/bench_test.go`
- Modify: `Makefile` (`microbench`: `go test -run '^$' -bench . -benchmem ./internal/server ./internal/resp ./internal/storage`)

- [ ] **Step 1: Write benchmarks** (package `server`): `BenchmarkExecGet`, `BenchmarkExecSet` (via `Session.Exec`, `b.ReportAllocs()`), `BenchmarkProcessCommandGet` (pre-built `[][]byte` argv, fake client with discard writer, calls `s.processCommand` directly — measures dispatch cost without RESP parse), `BenchmarkParallelGet` / `BenchmarkParallelSet` with `b.RunParallel` over 10 000 keys, `BenchmarkParallelHotKeyGet` (all goroutines read one key — exposes `touch()` contention), `BenchmarkPipeline16` (one TCP client via `testutil`, 16 commands per write). In `internal/resp`: `BenchmarkReadCommand`, `BenchmarkWriteBulk`. In `internal/storage`: `BenchmarkShardOf`, `BenchmarkLookup`, `BenchmarkSet`.
- [ ] **Step 2: Run** `make microbench | tee bench/results/microbench-baseline.txt` — Expected: output with ns/op and allocs/op for each; commit the file.
- [ ] **Step 3: Commit** — `git add internal/server/bench_test.go internal/resp/*_test.go internal/storage/*_test.go bench/results/microbench-baseline.txt Makefile && git commit -m "bench: microbenchmarks for dispatch, parsing and storage with baseline numbers"`

---

### Task 17: README rewrite

**Files:**
- Modify: `README.md` (full rewrite), `CLAUDE.md` (drop the "README is aspirational" note; add `make` targets and `test/e2e` module note)

- [ ] **Step 1: Write README.md** with sections, in this order: one-paragraph description (Redis 8.4-compatible, Go, stdlib-only, Linux/macOS/Windows Server, multi-core execution); **Status** table (implemented / partial / planned, from spec 2.1 — cluster, ACL, TLS as planned); **Quick start** (`go run ./cmd/distcache-server`, `redis-cli`, Docker); **Configuration** (redis.conf style, CLI flags, link to `CONFIG GET *`); **Architecture** with one Mermaid diagram of the request path (client → RESP → dispatch → shard locks → handler → propagation) and one of persistence/replication; **Compatibility** (RESP2/RESP3, link to `testutil.KnownDivergences` as the intentional-divergence list, DCSNAP ≠ RDB); **Performance** (how to run `make bench`, link to latest `bench/results/*.md`, a short honest note that pipelining/concurrency is required for high throughput); **Testing** (`make test`, `make test-e2e`, differential tests need `redis-server`, `DISTCACHE_SKIP_REDIS`); **Project layout**; **Roadmap** (six sub-projects, link to the spec); **Credits** — a short list that includes Redis (https://github.com/redis/redis) as the reference implementation and the video https://www.youtube.com/watch?v=sRXIRikME14&t=178s (one neutral line: "inspiration and background"); **License**. Keep the whole file under 250 lines; no claims the code does not support.
- [ ] **Step 2: Verify** every command in README runs: execute the quick start and `make` commands listed. Expected: all succeed.
- [ ] **Step 3: Commit** — `git commit -am "docs: rewrite README to match the implementation"`

---

### Task 18: Naming and documentation pass

**Files:**
- Modify: every file under `internal/`, `pkg/`, `cmd/` (rename only; no behaviour change)

- [ ] **Step 1: Inventory** — Run: `grep -rnE '^\s*(prop|repl|ks|cl|lns|idx|wmu|snapMu)\b' internal | head` and `go vet ./... && golangci-lint run ./...`; list every exported identifier lacking a doc comment with `go doc -all ./... 2>/dev/null | grep -c ''` as a before/after count (or `revive -rule exported` if available).
- [ ] **Step 2: Rename** the `Server` fields `ks→keyspace`, `prop→propagator`, `repl→replication`, `cmds→commands`, `lnMu/listeners` keep; `Client.r/w→reader/writer`; `Ctx.cl→client`, `Ctx.s→server`; `execUnit→executionUnit`; `propCmd→propagatedCommand`; `regSpec→commandSpec`. Use `gopls rename` (`gopls -rpc.trace rename`) or `gofmt -r` so references update everywhere, including tests written in Tasks 2–16.
- [ ] **Step 3: Document** — add file-header comments and doc comments per the Global Constraint; keep comments factual (what/why), no narration of the obvious.
- [ ] **Step 4: Run** `make lint && make test-all` — Expected: PASS, no behaviour change (`git diff --stat` shows only renames/comments).
- [ ] **Step 5: Commit** — `git commit -am "refactor: idiomatic Go names and doc comments across packages"`

---

### Task 19: Final verification and branch wrap-up

- [ ] **Step 1: Run** `make lint && make test-all && make cover` — Expected: all green; print `go tool cover -func=coverage.out | grep -E 'internal/(resp|storage|ds|config)|internal/server' | tail -5` and confirm the floors in Global Constraints.
- [ ] **Step 2: Push the branch and confirm CI is green on both OSes** (`gh run watch`).
- [ ] **Step 3:** Invoke `superpowers:finishing-a-development-branch`.
