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
        L[TCP listener] --> G1[client goroutine]
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
    B -- yes --> X[take every shard lock<br/>inside the snapshot barrier<br/>FLUSHALL, SWAPDB, EVAL, MULTI/EXEC]
    B -- no --> C{global?}
    C -- yes --> Y[handler walks the shards itself<br/>KEYS, SCAN, DBSIZE]
    C -- no --> E[compute key positions or KeysFunc<br/>→ set of shard indexes]
    E --> F{write flag?}
    F -- yes --> W[write locks, ascending index]
    F -- no --> RL[read locks, ascending index]
```

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

`BGSAVE` takes a copy-on-write snapshot. While it runs, every write handler
calls `DB.Preserve(key)` before it changes a key, so the snapshot still sees
the old value. Commands that take every shard lock (`lockall`) wait at the
snapshot barrier.

## 5. Replication

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
Known gap (B27): the snapshot does not store the replication ID and offset,
so a restarted replica always does a full resync.

## 6. Code map

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
| Web console (planned) | `web/console` |
| Deployment, scripts | `deploy/docker`, `scripts/bench.sh` |

## 7. Where this is heading

| # | Sub-project | Status |
|---|---|---|
| 1 | Test harness, baseline benchmarks, README | in progress |
| 2 | Single-node throughput: no global propagator lock, sharded counters, pooling, lock-free reads | planned |
| 3 | Redis Cluster–compatible cluster mode (16384 slots, MOVED/ASK, its own cluster bus) | planned |
| 4 | Hardening: ACL, automatic AOF rewrite, TLS, FAILOVER, gating `DEBUG` | planned |
| 5 | Client examples (Go, Java, Python, Node/TS) and an HTTP/WebSocket gateway | planned |
| 6 | Numbered technical guides | planned (this is Guide 1) |
| 7 | React web console: browse keys, view live metrics, visualise data | planned |

Today every write passes through one propagator mutex (`propagator.mu`). That is the
main contention point sub-project 2 removes.
