# Code review, 2026-11-23

A full review of the code on `master` (commit `95f5d4d`): six reviewers, one per
area, each reading the code and reproducing what they found against a running
server. This page records every finding and what happened to it. The fixes are
on the `release-readiness` branch.

## Result

| Severity | Found | Fixed | Open |
|---|---:|---:|---:|
| Critical | 7 | 7 | 0 |
| High | 11 | 11 | 0 |
| Medium | 17 | 11 | 6 |
| Low | 16 | 4 | 12 |

Every fix has a regression test that failed before the change. After the fixes
the whole suite passes with the race detector, including the scripts that
compare replies with a real Redis 8.4.8, and so do the end-to-end tests.

What the review did not cover in depth: the reviewers checked the key
positions of the suspicious entries in `commands.go`, not all 247, and they
tested the data structures in `internal/ds` through commands rather than by
reading every line. Nothing was run on Windows.

## Critical: a client could crash, hang or empty the server

| ID | Problem | Fix |
|---|---|---|
| CORE-1 | A subscriber that stopped reading overflowed its pub/sub queue; the queue was closed twice and the panic killed the process. | The queue is closed in one place, once (`closePushLocked`). |
| CORE-3 | `MULTI`, `MEMORY USAGE k`, `EXEC` read the key without a shard lock while others wrote, and Go aborted with "concurrent map read and map write". | `EXEC` now locks keys that a command names through a subcommand (`MEMORY USAGE`, `OBJECT`, `XINFO`, `XGROUP`). |
| LRC-1 | A Lua metatable whose `__index` pointed back at itself recursed until the Go stack overflowed, which cannot be recovered. So did deeply nested expressions, `__call` loops and long `..` chains. | Metatable chains, call depth and syntax nesting are bounded as in reference Lua; each case is now an ordinary script error (34 hostile scripts in the test). |
| DUR-1 | Recovering an append-only file with a cut tail removed valid commands, and the next restart failed with "bad AOF format". | The tail is truncated exactly after the last complete command or `MULTI`…`EXEC` unit. A test cuts a real file at every byte offset. |
| CMDA-1 | `SET` failed with `WRONGTYPE` when the key held a list, hash, set or stream. Redis overwrites. | Only `SET … GET` checks the old value's type. |
| — | With `requirepass` set, the server came back **empty** after a restart: the AOF loader's internal client was unauthenticated, so every replayed command was refused. Found while testing the Compose file. | The loader is authenticated, and replay now logs an error if any command is refused. |
| — | `MULTI`, `SET k v`, `DEBUG DIGEST`, `EXEC` deadlocked the whole server, and `DEBUG` was open to every client. | `DEBUG` needs `enable-debug-command` (off by default, as in Redis) and cannot be queued in a transaction. This also closes the earlier known bug B28. |

## High

| ID | Problem | Fix |
|---|---|---|
| CORE-4 | A client that disconnected while blocked in `BLPOP` was not noticed if it had sent more bytes after the command. The next pushed element was handed to the dead client and lost. | The blocked client's socket is watched to end of file; pipelined input is kept. |
| CORE-2, WEB-1 | Writes acknowledged during a shutdown were lost: clients kept executing while the final save ran. A test lost about 8,000 acknowledged `INCR`s. | Shutdown stops command execution first, then saves or syncs the AOF, then exits. |
| DUR-2 | `RESTORE` with a small forged payload made the decoder allocate up to 4 GiB. | Every declared length is checked against the bytes actually present. |
| DUR-3 | A replica with `appendonly yes` never rewrote its AOF after a full sync, so a restart lost the synced data (101 keys became 1). | The replica rewrites its AOF from the loaded snapshot before applying the stream. |
| WIN-1 | `BGREWRITEAOF` renamed a file over one that was still open, which always fails on Windows. | Both handles are closed before the rename on every platform. Verified by build and vet only. |
| CMDA-2 | `CONFIG SET dir` and `dbfilename` at runtime let a client make `SAVE` write to any path. | `enable-protected-configs` (default `no`) guards them, as in Redis. |
| CMDA-3 | `protected-mode` was accepted and ignored, so a server without a password served any host that could reach it. | Protected mode is implemented and on by default: without a password, only loopback clients are served. |
| CMDB-1 | `XCLAIM` and `XAUTOCLAIM` were replicated word for word. A replica or an AOF replay re-ran the idle-time check on its own clock and claimed different entries. | Each claimed entry is propagated as a forced `XCLAIM` with an absolute time and delivery count. |
| — | `XAUTOCLAIM … COUNT 2` claimed every pending entry it scanned. Found while verifying CMDB-1 against Redis. | `COUNT` bounds claimed plus deleted entries and the cursor resumes after them, as in Redis. |
| LRC-2 | The protocol reader allocated a bulk's declared length before any byte arrived. Eight connections sending 25 bytes each held 4.3 GB. | Large bulks are read in chunks, so memory follows the bytes received. |
| LRC-3 | A script could replace a global such as `tostring` with `rawset(_G, …)`, and later scripts from other clients saw the change. | The globals table is read-only to scripts, as in Redis 7 and later. |

## Medium and low, fixed

| ID | Problem and fix |
|---|---|
| CORE-5 | Before `AUTH`, a client could send commands of any size. Now 10 arguments and 16 KiB per argument, as in Redis. |
| CMDA-4, CMDA-5 | `SETRANGE` and `SORT … LIMIT` panicked (recovered) on offsets near the int64 limit. The bounds checks no longer overflow. |
| DUR-4, DUR-5 | A cut inside a command header, or inside a transaction, left bytes that corrupted later appends. Fixed with DUR-1. |
| DUR-6 | AOF write and fsync errors were ignored. Writes are now refused with `MISCONF` until the log can be written again. |
| LRC-4 | `CONFIG REWRITE` dropped comments, `include` and `rename-command` lines. It now keeps every line it does not own. |
| LRC-5 | `maxmemory 99999999999gb` overflowed to a negative number and the rewritten config could not start the server. Overflow is rejected. |
| LRC-6 | `rename-command X ""`, the usual way to disable a command, failed at startup. |
| LRC-7 | `SCRIPT FLUSH` swapped the interpreter pool while scripts used it. The swap is atomic. |
| WEB-5 | The container image ran as root with an unpinned toolchain and CI never built it. It now runs as uid 65532 on a pinned Go version, and CI builds and pings it. |
| CMDB-2 | `SINTERCARD` and `ZINTERCARD` rejected a repeated `LIMIT`. |
| DUR-9, WIN-2 | No directory fsync after renaming a snapshot or AOF. Added on Unix. |
| LRC-8 | `AUTH` compared passwords in variable time. It now compares digests in constant time. |

## Open

These are recorded for later work. None of them crashes the server or loses
acknowledged data in normal operation.

| ID | Severity | Problem |
|---|---|---|
| DUR-7 | medium | A full sync has one fixed 60 s deadline, so a very large dataset over a slow link can loop on full resyncs. |
| DUR-8 | medium | A replica that does a full resync keeps its own sub-replicas connected; they should be dropped and resynced. |
| CMDA-6 | medium | Redis 8.4 additions are missing: `MSETEX`, `DELEX`, `DIGEST`, `SET … IFEQ/IFNE/IFDEQ/IFDNE`. See [redis-compatibility.md](redis-compatibility.md). |
| WEB-2 | medium | Web console: the dashboard's memory control queues its `CONFIG SET` into a transaction the user left open in the console. |
| WEB-3 | medium | Web console: the command deny list checks names, so a command renamed with `rename-command` passes. |
| WEB-4 | medium | Web console: `WAIT` and `WAITAOF` can block a console session with nothing to wake it. |
| CMDA-7 | low | A failed foreground `SAVE` puts the server into `MISCONF` until a save succeeds. |
| CMDA-8 | low | `PFCOUNT` on a corrupt sparse HyperLogLog returns 0 instead of `INVALIDOBJ`. |
| CMDA-9 | low | `CONFIG GET` returns an empty string for parameters that are accepted and ignored. |
| LRC-9 | low | `#{1,2,3,nil,5}` is 3; Lua 5.1 gives 5. |
| WEB-6 | low | Web console: the 64 KiB command limit is not applied to non-htmx posts. |
| WEB-7 | low | A second `SIGTERM` is ignored, so a stuck shutdown needs `SIGKILL`. |
| WEB-8 | low | `distcache-bench` measures latency per batch from send time and has no socket deadlines. |
| WEB-9 | low | Web console: the self-signed certificate is renewed only at startup. |
| WEB-10 | low | Web console: the global login limit lets an attacker lock everyone out for a minute at a time. |
| — | low | A blocked client that pipelines more than `client-query-buffer-limit` is not seen disconnecting until it wakes. |
| — | low | A partially written AOF record is reported through `MISCONF` but not truncated until the next restart. |
| — | low | `RESTORE` with a malformed payload replies "DUMP payload version or checksum are wrong"; Redis says "Bad data format". |

The three medium console items must be fixed before the console is started by
the server (task 9 of the console plan). Known items from earlier work that
are still open: B26 (blocked-client fairness against non-blocked poppers),
B27 (a restarted replica always does a full resync), B42 (`COMMAND DOCS` has
no argument syntax) and B44 (`INFO` has no `latencystats` section).
