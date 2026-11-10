# Benchmark results

Each file is `<date>-<host>.md`, written by `make bench` (`bench/run.sh`).

The matrix is pipeline `{1, 16}` x clients `{50, 500}`, running
`set,get,incr,lpush,sadd,hset,zadd` with `-n 1000000 -r 100000`. DistCache runs
on port 6390 and, if `redis-server` is on PATH, Redis on 6391 with persistence
off. The same `distcache-bench` binary drives both, so numbers are comparable.
The ratio column is DistCache / Redis; it is `n/a` when Redis was not available.

Reproduce: `make bench` (set `BENCH_N=200000` for a quicker run). Latency
percentiles are per pipeline batch, not per command.

The earliest file is the baseline later performance work must beat. Compare
only results from the same machine.

## Environment variables for `bench/run.sh`

- `BENCH_N`: requests per test (default 1000000).
- `BENCH_PREBUILT=1`: skip `go build` and use the existing `bin/` binaries,
  for example inside a container without Go. `BENCH_GO_VERSION` then names
  the toolchain that built them.
- `BENCH_HOST`: name used in the result file instead of `hostname -s`
  (containers have meaningless hostnames).
- `BENCH_NOTE`: free text recorded in the results header describing the setup.

Before each matrix cell the script runs `FLUSHALL` on both servers so no cell
inherits data structures grown by earlier cells. It refuses to start if
something already answers on port 6390 or 6391.

## Variance caveat

Each cell is a single run with no repeats, so there is no variance estimate;
treat differences below roughly 10-15% as noise. Cells with 500 clients and
pipeline 16 are saturated (p99 in the hundreds of milliseconds) and noisier
still. When the load tool and both servers share one machine, or one Docker VM,
they compete for CPU, and Redis's single-threaded command execution is paired
with a multi-threaded load generator. The committed baseline
`2026-11-05-docker-linux-arm64-m1.md` was produced in a Docker Desktop VM and
predates the per-cell flush, so its later cells ran on larger data structures.
Compare results only from the same machine and setup.
