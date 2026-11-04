# SDD ledger — plan: docs/superpowers/plans/2026-11-04-test-harness-and-baseline.md
Spec: docs/superpowers/specs/2026-11-04-distcache-roadmap-and-test-harness-design.md
Branch: test-harness (isolated from master); merge base 5f9106b

## Preflight scan
| Tasks | Shared file/interface | Finding |
|---|---|---|
| 1 vs 17a | CI windows job; scripts/test.ps1 created in 17a | Task 1 CI lists windows-latest but ps1 doesn't exist yet → ruling R1 |
| 1 vs 14/15/16 | Makefile placeholders | consistent (later tasks replace placeholders) |
| 2 vs 14 | panic recovery ↔ DEBUG PANIC | consistent |
| 3 vs 18 | Client.flush / wmu rename | consistent if 18 renames after |
| 4 vs 10,16 | Session.Exec | consistent |
| 5 vs 10,14,15 | testutil StartServerOpts/FakeClock | consistent |
| 10 vs 11-13 | Step/S/RunScript/Differential/KnownDivergences | consistent |
| Global naming constraint vs 2-17 vs 18 | renames "when a task modifies the declaring file" vs repo-wide pass in 18 | conflict → ruling R2 |
| 12 | HGETDEL/HGETEX/HSETEX now compared vs Redis 8.4 | consistent with target |
| each task self-consistency | files created vs touched | 14 also modifies cmd_server.go (listed in commit step) — ok |

Ruling R1: Task 1's Windows CI job runs `go vet ./...` and `go test -race -count=1 ./...` directly (no make); 17a switches it to scripts/test.ps1 — why: ps1 doesn't exist until 17a — cost if wrong: one CI edit.
Ruling R2: Tasks 2–17 add doc comments to code they write but do NOT rename existing identifiers; all renames happen in Task 18 — why: keeps each task's diff reviewable and avoids rename churn across concurrent test-writing tasks — cost if wrong: Task 18 diff is larger.
Ruling R3: commits use `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (current attribution) instead of the plan's Fable line — why: system attribution changed with model — cost: none.

## Progress
Task 1: dispatched (base 3d55c25, implementer a30a57d9812ce7886, sonnet)
Task 1: implemented 0e0bf60; review: Critical CI services on non-Linux legs; Important unpinned lint, blanket errcheck/unused exclusions. Fix round 1 dispatched (resume a30a57d9812ce7886).
Task 1: minor (deferred): Dockerfile lacks -trimpath/-ldflags; ⚠️ windows -race gcc + golang:1.27 image unverified until CI runs.
Task 1: fix round 1/5 (4 addressed, 0 open; commits 0e0bf60..3599a17)
Task 1: minor (deferred): unused-field lint rules not path-scoped; imprecise best-effort comments in replication.go:557 & cmd/*/main.go; config.Rewrite leaves .tmp on rename failure
Task 1: complete (commits 3d55c25..3599a17, review clean)
Task 2: dispatched (base 3599a17, implementer a41f11e69a447c1a4, sonnet)
Ruling R4 (user direction 2026-11-04): Windows deferred/optional. Task 17a removed from plan. CI windows job must become compile+vet only (`go vet ./...` with GOOS=windows on ubuntu, or windows runner running `go build ./... && go vet ./...` without tests) — carried into the next implementer dispatch (Task 3) as a small extra change — cost if wrong: re-add the test job later.
Task 2: implemented e114706; review spec ✅, Important: partial reply desync on panic; minors: WARN on divergence, EXEC/EVAL panic tests, doc serveClient lock limit. Fix round 1 dispatched (fix base 4d779d3).
Task 2: fix round 1/5 (4 addressed, 0 open; commits 4d779d3..e8a773e)
Task 2: minor (deferred): closeAfterReply (flushed-then-panic) branch untested; Writer.nflush plain uint64 racy if another goroutine flushes same writer → carried into Task 3
Task 2: complete (commits 3599a17..e8a773e, review clean)
Task 3: dispatched (base d3a2ea8, implementer a51b4a759375d8d9f, sonnet; carries R4 CI change + atomic nflush)
Task 3: implemented d3a2ea8..8c593e2; review spec ✅, Important: W.Mu nil-swap in subscribe fragile; missing server-level subscribe+large push test. Fix round 1 dispatched (fix base 8c593e2).
Task 3: fix round 1/5 (3 addressed, 1 new Critical — ack-before-register loses publishes in window; caused by controller's round-1 suggestion; commits 8c593e2..82cca92)
Task 3: fix round 2 dispatched: lock wmu → hubAdd → ack → FlushLocked; no-loss test (fix base 82cca92)
Task 3: fix round 2/5 (1 addressed, 0 open; commits 82cca92..35d05b9)
Task 3: minor (deferred): loss test can't practically fail on old window — add a test hook delay or loop; reflowed comment in pubsub_write_test.go:100; queued-path (MULTI/script) subscribe ack can trail a push (pre-existing)
Task 3: complete (commits d3a2ea8..35d05b9, review clean)
Task 4: dispatched (base 35d05b9, implementer a39f53767366f7363, sonnet)
Task 4: implemented 35d05b9..53a9def; review spec ✅, Important: secondArg panics on short args; Close doesn't release blocked Exec; reply/push confusion after SUBSCRIBE (add ReadPush). Minor: vacuous CloseUnwatches test. Fix round 1 dispatched (fix base 53a9def).
Task 4: minor (deferred): XREADGROUP group named "block" + consumer "0" false ErrWouldBlock; WAIT/WAITAOF timeout 0 with replicas would hang Exec (not FlagBlocking)
Task 4: fix round 1/5 (4 addressed, 0 open; commits 53a9def..ab65d7c)
Task 4: minor (deferred): Exec after Close doesn't error (SUBSCRIBE/WATCH after Close re-registers state); ReadPush same-goroutine assumption not on its own doc
Task 4: complete (commits 35d05b9..ab65d7c, review clean)
Ruling R5: Task 5 clock has ONE source of truth: storage.Keyspace holds the clock (field now func() time.Time + method Now()); Server exposes SetClock(func() time.Time) (call before Start) and a Now() method delegating to keyspace. Plan's separately-assigned Server.Now/Keyspace.Now fields would let them drift if a test sets only one — cost if wrong: testutil uses SetClock instead of field assignment.
Task 5: dispatched (base ab65d7c, implementer a5f36adaca7201ed1, opus — 71 call sites, judgment)
Task 5: minor (deferred → carry to Task 7): TestClockJumpBackwardIsSafe may pass vacuously (Hz 10, 50ms sleep) — set Hz=100 / sleep ≥150ms; LFU decay wraparound on backward clock (same as Redis); SetClock before-Start not enforced
Task 5: complete (commits ab65d7c..2741dcc, review clean)
Ruling R6: test-writing tasks (6–13) do NOT fix production bugs they discover; they add a skipped regression test (t.Skip("known bug: ...")) and report it; the controller batches discovered bugs into a dedicated fix task after Task 13 — why: keeps test tasks reviewable and stops scope creep — cost if wrong: bugs stay open a few tasks longer.
Task 6: dispatched (base 2741dcc, implementer a3443aa4a1eb35364, sonnet)
BUG B1 (from Task 6): resp.ReadValue doesn't bound aggregate/bulk lengths → panic makeslice / multi-GB alloc on hostile input (ReadCommand is bounded). Used by replica reading master + clients. Skipped test TestReadValue_HugeLengthRegression.
BUG B2 (from Task 6): ReadValue doesn't verify CRLF after bulk payload.
Task 6: implemented 2741dcc..a69242d; review spec ✅, Important: fuzz skip (≥7 digits) too broad. Fix round 1 dispatched (fix base a69242d).
Task 6: fix round 1/5 (2 addressed, 0 open; commits a69242d..ea1e68f)
Task 6: minor (deferred): `|` attribute type missing from hasHugeLengthHeader marker set (also unbounded in reader.go ~196 — part of B1); mid-line header bypass via unchecked bulk CRLF (B2)
Task 6: complete (commits 2741dcc..ea1e68f, review clean)
Task 7: dispatched (base ea1e68f, implementer ae5caac1a4db7fa5c, sonnet; carries Task 5 backward-clock test fix)
Task 7: implemented ea1e68f..2b3d2e5; review spec ✅, Important: snapshot point-in-time test nondeterministic (could pass with Preserve no-op). Fix round 1 dispatched w/ mutation check (fix base 2b3d2e5).
Task 7: fix round 1/5 (2 addressed, 0 open; commits 2b3d2e5..b337ba6)
Task 7: complete (commits ea1e68f..b337ba6, review clean)
Ruling R7: Tasks 8 and 9 batched into one implementer dispatch + one review (same shape: test-only, independent packages ds / config+logger) — cost if wrong: one larger review.
Tasks 8+9: dispatched batched (base b337ba6, implementer a910901eeb6724ee3, sonnet)
BUG B3 (from Task 8, to confirm): ds.Match("*","") returns false; Redis stringmatchlen returns 1 for pattern "*" on empty string (if patternLen==1 return 1) → KEYS/SCAN MATCH/PSUBSCRIBE on empty key/channel diverge.
BUG B4 (from Task 9): config.LoadFile sets ConfigFile to the last opened file; after `include`, CONFIG REWRITE writes to the included file.
Tasks 8+9: implemented b337ba6..70e21b0; review: Critical R6 violation (B4 masked by t.Logf); Important list_test emulated LREM logic. Fix round 1 dispatched (fix base 70e21b0).
BUG B3 status: reviewer reads glob.go as faithful to stringmatchlen (false for "*" vs ""), unverified against Redis source → Ruling R8: resolve by differential test in Task 11 (KEYS * with empty key; PSUBSCRIBE * receiving on channel "") — cost if wrong: one more bug in the fix batch.
Tasks 8+9: fix round 1/5 (3 addressed, 0 open; commits 70e21b0..6135ee4)
Task 8: complete (commits b337ba6..6135ee4, review clean)
Task 9: complete (commits b337ba6..6135ee4, review clean)
Task 10: dispatched (base 6135ee4, implementer ae3f816882c5f0c6e, sonnet). NOTE: no redis-server locally, Docker daemon not running → differential tests skip locally; asked user.
Task 10: implemented 6135ee4..e5095d0; review spec ✅, Important: allowlist hides error-path diffs; Differential skips DistCache side when Redis absent (gate depends on Redis). Minors: IOERR both sides passes; drift between sequential runs; gate vs t.Parallel.
Ruling R9 (API, before Tasks 11–13): allowlist applies only to non-error replies — whenever EITHER side's reply is an error, full text is compared even for allowlisted commands (no Step.Strict flag; fewer knobs) — why: error strings are part of the compatibility contract and unsupported-subcommand errors are divergences we want surfaced — cost if wrong: a few extra steps need adjusting in Tasks 11–13. Also: Differential executes each step on DistCache then Redis before the next step (interleaved, kills TTL drift); IOERR on either side fails the test; Redis absent → still run DistCache side (records coverage), then t.Skip; package forbids t.Parallel (documented in gate file).
Task 10: fix round 1 dispatched (fix base e5095d0)
Task 10: fix round 1/5 (5 addressed, 0 open; commits e5095d0..f5d616f)
Task 10: complete (commits 6135ee4..f5d616f, review clean)
Task 11: dispatched (base f5d616f, implementer a21760bd3d1f4fcdd, opus — expected values written from Redis 8.4 semantics since no local Redis; R8 B3 steps included)
BUGS B5–B12 (from Task 11): B5 INCRBYFLOAT inf error text; B6 LCS on non-string → WRONGTYPE expected; B7 RANDOMKEY returns nil with one live key (~2%, cmd_keys.go:282) — real correctness bug; B8 CLIENT GETNAME ignores extra args; B9 CLIENT KILL TYPE foo → :0 instead of error; B10 CONFIG REWRITE error prefix; B11 ACL LOG RESET → *[] instead of +OK; B12 ACL unknown subcommand error text. All have skipped tests.
B3: closed — not a bug (KEYS */SCAN MATCH * return empty key; lone * short-circuits like Redis). Ruling: keep the ds.Match characterization case.
B7 cause (reviewer): RANDOMKEY samples 4×NumShards random shards with no fallback pass; miss prob ≈ e^-4 ≈1.8% with one key — fix: ordered scan fallback when DBSize>0.
Task 11: minor (deferred): cmd_keys_test.go:124 SCAN without COUNT relies on <10 keys; B12 note: custom message ok for real-but-unsupported subcommands
Task 11: complete (commits f5d616f..287cf9f, review clean)
Task 12: dispatched (base 287cf9f, implementer a8e22d9e3639d8aee, opus; includes Task 8 deferrals)
BUGS B13–B19 (from Task 12): B13 blocked clients woken all at once → not FIFO (infra.go blocker) — correctness/fairness; B14 XADD/XTRIM MAXLEN+MINID error text; B15 XREADGROUP explicit ID includes pending entry equal to ID — correctness; B16 XGROUP SETID/CREATECONSUMER/DELCONSUMER NOGROUP text; B17 HLL never sparse (memory/encoding divergence); B18 corrupt sparse HLL → WRONGTYPE not INVALIDOBJ; B19 GEOADD XX+NX/bad argc error text. Candidate B20: GEORADIUSBYMEMBER STOREDIST self-distance 8e-14 vs 0 (differential only).

Task 12: complete — 27b0c88 — review approved (0 C/0 I); minors: fill geoUnverifiedScript Wants after CI; make lint needs $HOME/go/bin on PATH. B17 confirmed observable via GET/STRLEN/DUMP.

Task 13: impl 10a8398 (base 27b0c88), cov 80.5%, gate on. Bugs B21 lua-time-limit alias, B22 SYNC sends +FULLRESYNC. Ruling: SYNC follows Redis over brief — spec authority is Redis 8.4 compat — cost: none if Redis indeed omits it.
Task 13 review: approved, 1 Important (RESTORE dup-library text unverified). Ruling: assert prefix only, candidate B23, confirm in CI — cost if wrong: weaker assertion until CI. Fix round 1 dispatched (resume implementer).
Task 13: complete — 10a8398, 7089b88 (fix r1, re-reviewed inline by controller: 3 findings addressed). Next: bug-fix batch (per R6) brief task-bugfix-brief.md, BASE 7089b88.

Bugfix batch: impl 7089b88..7cedda5 (10 commits). Candidates logged: B24 PFCOUNT does not persist cardinality cache; B25 hll-sparse-max-bytes ignored; B26 B13 FIFO not enforced vs non-blocked poppers (Redis serves waiters before returning to other clients).
Bugfix review: changes requested — I1 hard-coded multibulk limit breaks internal readers (Lua/Exec >1M elems); minors: defer unregister, readBulk alloc, ReadSnapshot deadline, LuaTimeLimit alias comment. B13 FIFO: no defects. Fix round 1 dispatched (resume implementer, all 5).
Bugfix batch: complete — 9010f58..7cedda5 + 398f4d5 (fix r1; controller re-review: I1 fixed, regression test verified to fail without fix; minors addressed). Next: Task 14 e2e, BASE 398f4d5.

Task 14: impl d0758e7 (base 398f4d5). B27: replica restart → full resync (snapshot lacks repl id/offset). Ruling: binary built to temp dir not bin/ — keeps tree clean — cost none.
Task 14: complete — d0758e7 — approved (0C/0I). B28: DEBUG not gated by enable-debug-command (Redis 7+ default no) — security-relevant, fold into sub-project 4 hardening. Parked minors: TTL msg ±1s, truncated-tail no log assert, port reuse race.
Next: Task 15 bench, BASE d0758e7.
User request 2026-11-04: React web console (RedisInsight-like + better data viz) → new sub-project 7, own spec after sub-project 1.
