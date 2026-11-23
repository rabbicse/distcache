# What DistCache does

A list of everything in the server today, with its status. For the commands
themselves see [commands.md](commands.md); for what Redis has and DistCache
lacks see [redis-compatibility.md](redis-compatibility.md); for how the parts
fit together see [architecture.md](architecture.md).

Status words: **works** means implemented and covered by tests; **partial**
means some of it is missing, as noted; **built, not on** means the code is in
the repository but the server does not start it yet; **planned** means not
written.

## Data

| Facility | Status | Notes |
|---|---|---|
| Strings, counters, bit operations | works | `SET`/`GET` families, `INCR`, `APPEND`, `GETRANGE`, `LCS`, `BITFIELD`, `BITPOS` |
| Lists | works | Including blocking pops and `LMPOP` |
| Hashes | works | Including per-field expiry (`HEXPIRE`, `HGETEX`, `HSETEX`) |
| Sets | works | Including `SINTERCARD` |
| Sorted sets | works | Range by rank, score and lex; unions and intersections; blocking pops |
| Streams | works | Consumer groups, `XAUTOCLAIM`, trimming |
| HyperLogLog | works | `PFADD`, `PFCOUNT`, `PFMERGE` |
| Geospatial | works | `GEOADD`, `GEOSEARCH`, `GEORADIUS` families |
| 16 databases | works | `SELECT`, `MOVE`, `SWAPDB`, `COPY … DB` |
| Expiry | works | Per key and per hash field; expired keys are removed on access and by a background cycle |
| Eviction | works | `maxmemory` with eight policies: LRU, LFU, random and TTL, over all keys or only keys with an expiry |
| JSON, search, time series, probabilistic types, vector sets | planned | The Redis 8 module families |

247 commands in all.

## Concurrency

| Facility | Status | Notes |
|---|---|---|
| Multi-core command execution | works | The keyspace is split into shards (256 by default), each with its own read/write lock. Commands on different shards run at the same time; reads on the same shard run at the same time too. |
| One goroutine per connection | works | No event loop; a slow client does not delay others |
| Atomic multi-key commands | works | Shard locks are taken in a fixed order, so there are no deadlocks |
| Transactions | works | `MULTI`/`EXEC`/`DISCARD`/`WATCH`; the transaction holds its locks for its whole run |
| Blocking commands | works | `BLPOP`, `BLMOVE`, `BZPOPMIN`, `XREAD BLOCK`, `WAIT` and friends, with first-come-first-served wakeups |
| Pipelining | works | Replies are buffered and flushed when the pipeline drains |

## Scripting

| Facility | Status | Notes |
|---|---|---|
| `EVAL`, `EVALSHA`, `SCRIPT` | works | A Lua 5.1 interpreter written in Go, with `cjson`, `cmsgpack`, `bit`, `string`, `table` and `math` |
| Functions | works | `FUNCTION LOAD`, `FCALL`, `FCALL_RO`, flags, dump and restore |
| Atomic scripts | works | A script holds every shard lock while it runs; its writes are replicated as the commands it ran |
| Sandbox | works | Scripts cannot change globals or library tables, recursion and nesting are bounded, and a slow script makes the server reply `BUSY` after `lua-time-limit`; `SCRIPT KILL` stops one that has not written |
| Script debugger | planned | `SCRIPT DEBUG` |

## Messaging

| Facility | Status | Notes |
|---|---|---|
| Pub/Sub | works | Channels, patterns and sharded channels; a subscriber that falls too far behind is disconnected |
| Keyspace notifications | works | `notify-keyspace-events` |
| `MONITOR` | works | |
| Client-side caching | planned | `CLIENT TRACKING` |

## Durability

| Facility | Status | Notes |
|---|---|---|
| Snapshots | works | `SAVE`, `BGSAVE` and automatic `save` rules. `BGSAVE` is a point-in-time copy taken while writes continue. The file format is DistCache's own. |
| Append-only file | works | Every write is logged; `appendfsync` chooses between `always`, `everysec` and `no`. A cut tail after a crash is repaired on start. |
| Log compaction | partial | `BGREWRITEAOF` works; automatic rewrite is planned |
| Clean shutdown | works | On `SIGTERM` the server stops taking commands, then persists, so nothing acknowledged is lost |
| `DUMP` / `RESTORE` | works | Between DistCache servers only |
| Reading Redis RDB files | planned | |

## Replication

| Facility | Status | Notes |
|---|---|---|
| Primary and read-only replicas | works | `REPLICAOF`; replicas of replicas |
| Partial resync | works | After a short disconnect a replica continues from the backlog |
| Full sync | works | Snapshot, then the live stream |
| `WAIT`, `WAITAOF` | works | Wait for replicas or local fsync to confirm a write |
| Resync after a replica restart | partial | Always a full sync |
| Automatic failover, cluster mode | planned | |

## Security

| Facility | Status | Notes |
|---|---|---|
| Password | works | `requirepass`, `AUTH`, `HELLO … AUTH`; compared in constant time |
| Protected mode | works | With no password, only clients on the same machine are served |
| Limits before login | works | An unauthenticated client may send only small commands |
| Protected settings | works | `dir` and `dbfilename` cannot be changed at runtime unless `enable-protected-configs` allows it |
| `DEBUG` gate | works | Off unless `enable-debug-command` allows it |
| Disabling or renaming commands | works | `rename-command FLUSHALL ""` |
| ACL users and permissions | planned | Only the `default` user exists |
| TLS | planned | |

## Operations

| Facility | Status | Notes |
|---|---|---|
| Configuration | works | `redis.conf` syntax, `--name value` arguments, `include`, `CONFIG GET/SET/REWRITE`; most settings change without a restart |
| `INFO`, `SLOWLOG`, `LATENCY`, `MEMORY`, `CLIENT LIST/KILL/PAUSE` | works | `INFO` lacks the `latencystats` section |
| Unix socket | works | `unixsocket` |
| RESP2 and RESP3 | works | `HELLO 3` |
| Load tool | works | `distcache-bench`, with pipelining and percentile latencies |
| Web console | built, not on | Login, live dashboard and a command console exist under `internal/web`. The key browser, the Pub/Sub page and the switch that starts it are still to do. |
| Prometheus metrics | planned | |

## Packaging

| Facility | Status | Notes |
|---|---|---|
| Linux and macOS binaries (amd64, arm64) | works | `make dist` |
| Windows binaries (amd64, arm64) | partial | Built by `make dist`; a preview, not yet gated by the test suite |
| Container image | works | 7 MB, non-root, amd64 and arm64 |
| Compose file | works | One node, or a primary and a replica |
| Kubernetes manifests | works | kustomize: one node, or a primary and replicas. No failover. |
| systemd unit | works | Sandboxed, unprivileged user |
| Client examples | partial | Go, Java, Python and Node in `examples/`; only the Go one has been run so far |
