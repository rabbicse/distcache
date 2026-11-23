# Benchmarks: DistCache and Redis 8.4

Measured on 2026-11-23 against Redis 8.4.8. The raw result files are in
[bench/results](../bench/results/); every number here comes from them.

## The short answer

DistCache is **not faster than Redis overall** today.

- **Simple commands, no pipelining:** DistCache is 1.1 to 1.5 times faster
  than Redis in its default configuration.
- **Pipelined commands:** Redis is faster: by 10 to 25% on `SET`, `GET` and
  `INCR`, and by up to 3 times on the list, set, hash and sorted-set tests.
  Those four tests write to **one key**, which is the worst case for
  DistCache; spread over many keys they are within about 25% of Redis.
- **Redis with `--io-threads 4`:** Redis is faster in three of the four
  load shapes tested, by 1.3 to 4.8 times depending on the command.
- **Sorted-set writes (`ZADD`):** Redis is about 2 to 3.5 times faster when
  every client writes the same sorted set, and 10 to 25% faster over many.
- **When one client runs a slow command:** Redis stops serving everyone
  else until it finishes; DistCache keeps going. In that test DistCache
  served about 190 times more `GET`s per second than Redis.

So the multi-core design pays off for isolation between clients, not yet
for raw throughput. Closing the throughput gap is the next planned piece of
work (see the end of this page).

## How it was measured

- One machine: a Docker Desktop Linux VM with 8 vCPUs and 7.7 GiB on an
  Apple M1 MacBook Pro. DistCache, Redis and the load tool share those 8
  CPUs, so they compete with each other.
- The same load tool, `distcache-bench`, drives both servers: 1,000,000
  requests per test, 100,000 keys, 3-byte values, persistence off on both.
- Each cell is one run. Treat differences under about 15% as noise.
- `ratio` is DistCache divided by Redis: above 1 means DistCache is faster.

Reproduce with `make bench` (needs `redis-server` on the PATH) and
`scripts/bench-slow-command.sh`. The exact commands for a Docker run are in
[bench/results/README.md](../bench/results/README.md).

## Throughput against Redis, default configuration

Thousands of operations per second, and the ratio.

| Command | 50 clients | 500 clients | 50 clients, pipeline 16 | 500 clients, pipeline 16 |
|---|---|---|---|---|
| `SET` | 203 vs 158 (**1.28**) | 245 vs 186 (**1.32**) | 798 vs 965 (0.83) | 800 vs 922 (0.87) |
| `GET` | 182 vs 168 (1.08) | 285 vs 193 (**1.48**) | 868 vs 1,148 (0.76) | 1,149 vs 1,081 (1.06) |
| `INCR` | 162 vs 164 (0.99) | 269 vs 193 (**1.39**) | 900 vs 971 (0.93) | 830 vs 963 (0.86) |
| `LPUSH` | 206 vs 141 (**1.46**) | 228 vs 169 (**1.35**) | 448 vs 445 (1.01) | 401 vs 451 (0.89) |
| `SADD` | 202 vs 165 (**1.22**) | 185 vs 192 (0.96) | 370 vs 783 (0.47) | 333 vs 741 (0.45) |
| `HSET` | 192 vs 158 (**1.21**) | 201 vs 190 (1.06) | 304 vs 829 (0.37) | 321 vs 821 (0.39) |
| `ZADD` | 50 vs 108 (0.46) | 65 vs 115 (0.56) | 101 vs 239 (0.42) | 93 vs 281 (0.33) |
| Geometric mean | 1.04 | 1.11 | 0.64 | 0.63 |

Across all 28 cells DistCache is faster in 12 and the geometric mean of the
ratio is 0.83.

Source: [2026-11-23-docker-linux-arm64-m1.md](../bench/results/2026-11-23-docker-linux-arm64-m1.md),
which also has the latency percentiles.

## Throughput against Redis with threaded I/O

Redis can read and write sockets on extra threads (`--io-threads`); command
execution stays on one thread. With `--io-threads 4`:

| Load shape | Geometric mean of the ratio | Range | Cells where DistCache is faster |
|---|---:|---|---:|
| 50 clients | 1.16 | 0.41 to 1.53 | 6 of 7 |
| 500 clients | 0.62 | 0.32 to 0.77 | 0 of 7 |
| 50 clients, pipeline 16 | 0.48 | 0.27 to 0.75 | 0 of 7 |
| 500 clients, pipeline 16 | 0.38 | 0.21 to 0.58 | 0 of 7 |

The highest throughput seen in this run was Redis at 2.2 million `GET`s per
second (500 clients, pipeline 16); DistCache reached 1.2 million in the same
cell.

Source: [2026-11-23-docker-linux-arm64-m1-redis-io-threads-4.md](../bench/results/2026-11-23-docker-linux-arm64-m1-redis-io-threads-4.md).

## One slow command

50 clients run `SET` and `GET` while one more client runs a long read-only
command back to back: `SINTERCARD` over two sets of 500,000 members, tens
of milliseconds per run. First run of two:

| Server | Background | `GET` ops/s | `GET` p50 | `SET` ops/s | `SET` p50 |
|---|---|---:|---:|---:|---:|
| DistCache | none | 186,213 | 0.22 ms | 170,403 | 0.22 ms |
| DistCache | slow command loop | 185,772 | 0.23 ms | 17,959 | 0.21 ms |
| Redis | none | 89,551 | 0.45 ms | 98,091 | 0.43 ms |
| Redis | slow command loop | 978 | 51 ms | 792 | 54 ms |

Redis runs one command at a time, so every other client waits for each
`SINTERCARD` to finish. DistCache takes read locks on the shards that hold
the two sets and nothing else: reads everywhere continue, and writes
continue on every other shard (254 or 255 of the 256). Writes to keys on
the locked shards wait, which is why `SET` throughput drops while its
median latency does not.

The second run gave the same picture (DistCache 161,162 `GET`s per second
under the loop, Redis 936). The same reasoning applies to any long command
that touches few keys: a big `SORT`, `LRANGE 0 -1`, `SUNIONSTORE`, `KEYS`.
It does not apply to Lua scripts and `MULTI`/`EXEC` blocks that touch many
keys, which lock the shards they use for their whole run.

Source: [2026-11-23-slow-command-docker-linux-arm64-m1.md](../bench/results/2026-11-23-slow-command-docker-linux-arm64-m1.md).

## The collection tests write to one key

Like `redis-benchmark`, the load tool sends every `LPUSH`, `SADD`, `HSET`
and `ZADD` to a single key (`mylist`, `myset`, `myhash`, `myzset`). All the
clients then queue on one shard lock, so DistCache runs those tests on one
core and pays for handing the lock from goroutine to goroutine. A profile of
the pipelined `SADD`/`HSET` run shows the 50 clients waiting for that lock
for about 85% of the run.

The same four commands with the key chosen at random from 100,000 (a
one-off run with a patched load tool, Redis in its default configuration):

| Command | 50 clients | 500 clients | 50 clients, pipeline 16 | 500 clients, pipeline 16 |
|---|---|---|---|---|
| `LPUSH` | 166 vs 161 (1.04) | 185 vs 108 (**1.72**) | 822 vs 869 (0.95) | 976 vs 838 (**1.16**) |
| `SADD` | 151 vs 176 (0.85) | 220 vs 168 (**1.31**) | 698 vs 784 (0.89) | 753 vs 771 (0.98) |
| `HSET` | 184 vs 179 (1.02) | 161 vs 182 (0.89) | 687 vs 823 (0.83) | 752 vs 778 (0.97) |
| `ZADD` | 142 vs 165 (0.86) | 152 vs 168 (0.91) | 484 vs 642 (0.75) | 559 vs 627 (0.89) |

So one hot key is where DistCache is 2 to 3 times behind; on many keys it is
roughly level with Redis, not ahead.

## Where the time goes

CPU, mutex and blocking profiles of the server under these loads (Linux, in
the same VM) show four costs, largest first.

1. **Reading the clock.** A `SET` reads the system clock seven or eight
   times: for the idle timer, the command statistics, the expiry check and
   three times for the LFU counter. That is about 19% of CPU on pipelined
   `SET` and `GET`. Redis reads the clock once per event-loop cycle.
2. **Allocation and garbage collection.** Each command allocates its
   arguments, a context, a key string, a database handle and a propagation
   record. Allocation is about 16% of CPU on pipelined `SET` and the
   collector's background work another 14%.
3. **Lock handoff on a hot key**, described above.
4. **The append-only file.** With `appendonly yes` and `everysec`, pipelined
   `SET` throughput fell by about half in a local run. Every write queues on
   one mutex that orders the log (`propagator.mu`), and it does so while
   holding its shard lock, so the queue spreads to the shards. With
   persistence off that mutex is a minor cost.

Without pipelining the server is bound by system calls instead: one read
and one write per command are 53% of CPU with 500 clients. That is also
why Redis with I/O threads, which serves many sockets per wake-up, is ahead
at 500 clients.

A check with the server pinned to four CPUs and the load tool to the other
four gave the same picture as the shared runs above, so the gap is in the
server, not an artefact of sharing CPUs with the load tool.

The next sub-project targets these in that order: a cached clock, fewer
allocations per command, running a pipeline's commands for one shard under
one lock, a log that does not serialize every write behind one mutex, and
per-shard counters. The results on this page are the baseline it has to
beat.

## Limits of these numbers

- One machine, one run per cell, load tool and servers sharing CPUs. A
  dedicated load generator on a second machine would be fairer to both.
- Three-byte values and uniform random keys. Large values, hot keys and
  mixed workloads were not measured.
- Persistence was off. With `appendonly yes` both servers slow down.
- Latency is measured per pipeline batch from the moment it is sent, so
  stalls are under-reported for both servers (a closed-loop load tool does
  not issue the requests it would have sent during a stall).
- Redis ran inside the same VM from the official image; DistCache was built
  with Go 1.27.3 for linux/arm64.
