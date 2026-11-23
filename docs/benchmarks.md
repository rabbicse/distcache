# Benchmarks: DistCache and Redis 8.4

Measured on 2026-11-23 against Redis 8.4.8. The raw result files are in
[bench/results](../bench/results/); every number here comes from them.

## The short answer

DistCache is **not faster than Redis overall** today.

- **Simple commands, no pipelining:** DistCache is 1.1 to 1.5 times faster
  than Redis in its default configuration.
- **Pipelined commands:** Redis is faster, by up to 3 times on set, hash and
  sorted-set writes.
- **Redis with `--io-threads 4`:** Redis is faster in three of the four
  load shapes tested, by 1.3 to 4.8 times depending on the command.
- **Sorted-set writes (`ZADD`):** Redis is about 2 to 3.5 times faster in
  every test.
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

## Why throughput is not ahead, and what is planned

These are the known costs on the command path; they have not been profiled
one by one yet.

- Every write passes through one mutex that orders the replication and AOF
  stream (`propagator.mu`), even with persistence off. Writes on different
  shards therefore still queue behind each other.
- Each command takes and releases a shard lock and updates shared counters.
  Redis does neither.
- A pipeline of 16 commands is 16 separate lock round trips in DistCache.
  Redis runs the 16 back to back with no locking, which is why pipelining
  helps Redis more.
- The sorted set is slower than Redis's in every test, with or without
  contention.

The next sub-project targets exactly these: removing the global propagation
lock, per-shard counters, buffer pooling, lock-free reads and a faster
sorted set. The results on this page are the baseline it has to beat.

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
