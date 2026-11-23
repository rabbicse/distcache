# DistCache system design

This document describes how DistCache works today, from the TCP socket to the
disk and the replicas. The diagrams are written in Mermaid, so GitHub renders them inline.

## 1. The big picture

```mermaid
flowchart LR
    subgraph Clients
        C1[redis-cli / go-redis / Jedis / redis-py / ioredis]
    end
    subgraph DistCache["distcache-server (one process)"]
        L[TCP / Unix listener<br/>protected-mode check] --> G1[client goroutine]
        L --> G2[client goroutine]
        L --> G3[client goroutine ...]
        G1 & G2 & G3 --> D[Dispatcher<br/>command table + pre-exec checks]
        D --> KS[(Sharded keyspace<br/>256 shards × RWMutex)]
        D --> LUA[Lua 5.1 VM]
        D --> PS[Pub/Sub hub]
        D --> P[Propagator<br/>single ordered RESP stream]
        P --> AOF[(appendonly.aof)]
        P --> BL[Replication backlog]
        BL --> R1[replica sender]
        KS --> SNAP[(dump.dcs snapshot)]
        CRON[server cron<br/>active expiry, eviction, stats] --> KS
    end
    C1 -- RESP2/RESP3 over TCP --> L
    R1 -- PSYNC stream --> REP[(Replica DistCache)]
```

**The key idea:** Redis runs every command on one thread. DistCache gives each
connection its own goroutine and splits the keyspace into shards. Commands that
touch different shards run in parallel on all CPU cores. Commands on the same
shard are serialized by that shard's lock.

## 2. Request lifecycle

### End to end: what happens to one command

```mermaid
flowchart TD
    A[Client connects] --> B{protected mode on,<br/>no password,<br/>client not local?}
    B -- yes --> B1[reply -DENIED, close]
    B -- no --> C[client goroutine starts]
    C --> D[read one RESP frame<br/>size limits; tighter before AUTH]
    D --> E{command known,<br/>arity right,<br/>authenticated?}
    E -- no --> E1[error reply]
    E -- yes --> F{inside MULTI?}
    F -- yes --> F1[queue it, reply QUEUED]
    F -- no --> G{server state allows it?<br/>LOADING, BUSY script, READONLY replica,<br/>OOM, MISCONF after a failed save or AOF write}
    G -- no --> E1
    G -- yes --> H[lock the shards of its keys<br/>write or read locks, ascending order]
    H --> I[run the handler]
    I --> J{changed data?}
    J -- no --> M
    J -- yes --> K[propagate the effect, still holding the locks:<br/>AOF buffer + replication backlog]
    K --> K2[invalidate WATCHes, wake blocked clients,<br/>publish keyspace notifications]
    K2 --> M[unlock]
    M --> N[buffer the reply; flush when the pipeline is drained]
    N --> D
```

The same path serves commands replayed from the AOF at startup, commands
arriving from a primary, and commands sent by the web console through an
in-process session. Those internal clients are already authenticated, and the
first two also skip the state checks.

### One write in detail

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant G as serveClient goroutine<br/>(client.go)
    participant R as RESP reader<br/>(internal/resp)
    participant D as processCommand<br/>(dispatch.go)
    participant K as Shards<br/>(storage/keyspace.go)
    participant H as Handler<br/>(cmd_*.go)
    participant P as Propagator<br/>(propagate.go)

    C->>G: *3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n
    G->>R: read and parse a frame (size limits enforced)
    R-->>G: args = [SET k v]
    G->>D: look up the command and check arity, AUTH, MULTI queueing,<br/>LOADING / BUSY / READONLY / OOM / MISCONF
    D->>K: lock the key's shards (write or read lock, ascending order)
    D->>H: run the handler with *Ctx
    H->>K: mutate the entry, ctx.dirty++
    D->>P: flushUnit (still holding the locks → per-key order preserved)
    D->>D: post-processing: WATCH invalidation, wake blocked clients,<br/>keyspace notifications
    D->>K: unlock
    G-->>C: +OK\r\n (buffered, flushed when the pipeline drains)
```

### How the lock mode is chosen

```mermaid
flowchart TD
    A[command flags from commands.go] --> B{lockall?}
    B -- yes --> X[take every shard lock<br/>inside the snapshot barrier<br/>FLUSHALL, SWAPDB, EVAL, FCALL]
    B -- no --> C{global?}
    C -- yes --> Y[handler walks the shards itself<br/>KEYS, SCAN, DBSIZE]
    C -- no --> E[compute key positions or KeysFunc<br/>→ set of shard indexes]
    E --> F{write flag?}
    F -- yes --> W[write locks, ascending index]
    F -- no --> RL[read locks, ascending index]
```

`EXEC` locks the union of the shards its queued commands need (every shard
if one of them needs them all), including
keys that a command names through a subcommand (`MEMORY USAGE key`,
`OBJECT ENCODING key`), and holds them until the last command has run.

A wrong key specification in the command table means a command touches data
without holding its lock. Each entry in `commands.go` therefore deserves the
same care as the handler code.

## 3. Blocking commands (BLPOP, BLMOVE, XREAD BLOCK, ...)

```mermaid
sequenceDiagram
    participant A as Client A (BLPOP q 0)
    participant D as Dispatcher
    participant W as FIFO waiter queue per key
    participant B as Client B (LPUSH q x)
    A->>D: BLPOP q 0
    D->>D: handler finds q empty → ctx.blocked
    D->>W: register A, release locks, wait
    B->>D: LPUSH q x (dirty)
    D->>W: signal the first waiter on q
    W-->>D: wake A → re-run its handler under the locks
    D-->>A: 1) "q" 2) "x"
    Note over W: If the woken client is gone or still sees an empty key,<br/>the wakeup passes to the next waiter.
```

## 4. Durability

```mermaid
flowchart LR
    H[write handler] --> P[propagator.propagate<br/>SELECT + command, or MULTI ... EXEC]
    P --> M{appendfsync}
    M -- always --> S1[write + fsync<br/>before the reply]
    M -- everysec --> S2[append to buffer → writer goroutine<br/>group-writes within ~1 ms, fsyncs every 1 s]
    M -- no --> S3[buffer → write, the OS decides when to fsync]
    P --> BL[backlog → replicas]
    P --> RW[rewrite buffer while<br/>BGREWRITEAOF runs]
```

| Mechanism | File | When it is used |
|---|---|---|
| Snapshot (`DCSNAP` binary, not RDB) | `dump.dcs` | `SAVE`, `BGSAVE`, the AOF rewrite preamble, replica full sync, `DUMP`/`RESTORE` |
| Append-only file | `appendonly.aof` | Every write, if `appendonly yes` |
| Startup | — | Load the AOF if it is enabled, otherwise the snapshot |

On startup a cut or half-written tail of the AOF (the usual result of a
crash) is truncated at the end of the last complete command or
`MULTI`…`EXEC` block, and loading continues. If writing or syncing the AOF
fails while the server runs, writes are refused with `MISCONF` until the log
can be written again.

`BGSAVE` takes a copy-on-write snapshot. While it runs, every write handler
calls `DB.Preserve(key)` before it changes a key, so the snapshot still sees
the old value. Commands that take every shard lock (`lockall`) wait at the
snapshot barrier.

## 5. Shutdown

```mermaid
flowchart LR
    S[SIGTERM, SIGINT or SHUTDOWN] --> A[stop accepting connections]
    A --> B[stop command execution:<br/>new commands are refused,<br/>blocked clients are woken,<br/>running commands finish]
    B --> C{appendonly?}
    C -- yes --> D[flush and fsync the AOF, close it]
    C -- no --> E[write a snapshot<br/>if save rules are set]
    D & E --> F[close clients, exit]
```

Execution stops before anything is saved. That order guarantees that a write
which received a success reply is in the snapshot or the AOF.

## 6. Replication

```mermaid
sequenceDiagram
    participant Rp as Replica
    participant M as Master
    Rp->>M: PING, REPLCONF listening-port / capa
    Rp->>M: PSYNC <replid> <offset>
    alt offset still in the backlog
        M-->>Rp: +CONTINUE → stream from the backlog
    else unknown replid or offset too old
        M-->>Rp: +FULLRESYNC <replid> <offset>
        M-->>Rp: DCSNAP snapshot payload
        M-->>Rp: then the live command stream
    end
    loop every second
        Rp->>M: REPLCONF ACK <offset>
    end
```

Commands that arrive from the master skip the rejection checks, ignore expiry
(the master sends a `DEL` when a key expires), and are not propagated again.
Commands whose result depends on the clock or on chance are propagated as
their effect, not as typed: `EXPIRE` becomes an absolute `PEXPIREAT`, `SPOP`
becomes `SREM`, `XCLAIM` and `XAUTOCLAIM` become one forced `XCLAIM` per
entry with its delivery time and count, and a script becomes the commands it
ran. A replica with `appendonly yes` rewrites its own AOF from the snapshot
after a full sync.

Known gaps: the snapshot does not store the replication ID and offset, so a
restarted replica always does a full resync (B27), and there is no automatic
failover.

## 7. Security boundaries

| Boundary | Mechanism |
|---|---|
| Who may connect | `bind`; protected mode refuses non-local clients while no password is set |
| Who may run commands | `requirepass` + `AUTH`; before `AUTH` a command may have at most 10 arguments of 16 KiB |
| Dangerous commands | `DEBUG` needs `enable-debug-command`; `rename-command X ""` disables any command |
| Where files go | `dir` and `dbfilename` cannot be changed at runtime unless `enable-protected-configs` allows it |
| Scripts | Read-only globals and library tables, bounded recursion, `BUSY` after `lua-time-limit` |
| Hostile input | Bulk lengths and snapshot/`RESTORE` payload lengths are never trusted for allocation |

There is no TLS on the Redis port and there are no per-user permissions yet.

## 8. Web console

`internal/web` holds a built-in console: login against `requirepass`, a live
dashboard fed by server-sent events, and a command console. It talks to the
server through an in-process session (`server.Session`), so its commands
take the same path as any client's. The package is complete enough to test
but `distcache-server` does not start it yet; the key browser, the Pub/Sub
page and the start-up switch (`web-port`) are the remaining tasks. The design
is in [superpowers/specs/2026-11-10-web-console-design.md](superpowers/specs/2026-11-10-web-console-design.md).

## 9. Code map

| Layer | Package / file |
|---|---|
| Entry points | `cmd/distcache-server`, `cmd/distcache-bench` |
| Config (live, atomics), logging | `internal/config`, `internal/logger` |
| Wire protocol | `internal/resp` |
| Connection, dispatch, commands | `internal/server` (`client.go`, `dispatch.go`, `commands.go`, `cmd_*.go`) |
| Keyspace, eviction, codec | `internal/storage` |
| Data structures | `internal/ds` (list, hash, set, zset, stream, HLL, geo, glob) |
| Scripting | `internal/lua`, `internal/server/scripting.go` |
| Test helpers | `internal/testutil`, `test/e2e` |
| Client examples | `examples/` (Go, Java, Python, Node) |
| Web console | `internal/web` (templates and static files embedded in the binary) |
| Packaging | `deploy/docker`, `deploy/kubernetes`, `deploy/systemd`, `deploy/distcache.conf`, `scripts/release.sh` |
| Benchmarks, generated docs | `scripts/bench.sh`, `scripts/bench-slow-command.sh`, `scripts/gencommands` |

## 10. Where this is heading

| # | Sub-project | Status |
|---|---|---|
| 1 | Test harness, baseline benchmarks, README | done |
| 2 | Single-node throughput: no global propagator lock, sharded counters, pooling, lock-free reads, faster sorted set | planned, next |
| 3 | Redis Cluster–compatible cluster mode (16384 slots, MOVED/ASK, its own cluster bus) | planned |
| 4 | Hardening | partly done: protected mode, protected configs, the `DEBUG` gate. Planned: ACL users, TLS, automatic AOF rewrite, `FAILOVER` |
| 5 | Client examples (Go, Java, Python, Node) and a WebSocket gateway for browsers | examples written; gateway planned |
| 6 | Numbered technical guides | Guide 1 (manual testing) and Guide 3 (deployment) written |
| 7 | Built-in web console (htmx, embedded in the binary) | 7 of 10 tasks done; not started by the server yet |

Today every write passes through one propagator mutex (`propagator.mu`). That
is the main contention point sub-project 2 removes; the numbers it has to
beat are in [benchmarks.md](benchmarks.md).
