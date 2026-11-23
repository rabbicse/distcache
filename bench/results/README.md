# Benchmark results

Each file is `<date>-<host>.md`, written by `make bench` (`scripts/bench.sh`).

The matrix is pipeline `{1, 16}` x clients `{50, 500}`, running
`set,get,incr,lpush,sadd,hset,zadd` with `-n 1000000 -r 100000`. DistCache runs
on port 6390 and, if `redis-server` is on PATH, Redis on 6391 with persistence
off. The same `distcache-bench` binary drives both, so numbers are comparable.
The ratio column is DistCache / Redis; it is `n/a` when Redis was not available.

Reproduce: `make bench` (set `BENCH_N=200000` for a quicker run). Latency
percentiles are per pipeline batch, not per command.

The earliest file is the baseline later performance work must beat. Compare
only results from the same machine. [docs/benchmarks.md](../../docs/benchmarks.md)
explains the latest results.

## Result files

| File | What it is |
|---|---|
| `2026-11-23-docker-linux-arm64-m1.md` | Current matrix against Redis 8.4.8 in its default configuration |
| `2026-11-23-docker-linux-arm64-m1-redis-io-threads-4.md` | The same matrix against Redis started with `--io-threads 4` |
| `2026-11-23-slow-command-docker-linux-arm64-m1.md` | `scripts/bench-slow-command.sh`: SET/GET while another client runs a long command |
| `2026-11-05-docker-linux-arm64-m1.md` | First baseline (before the per-cell flush) |
| `microbench-baseline.txt` | Go micro-benchmarks (`make microbench`) |

## Running in Docker

The 2026-11-23 files were produced inside the `redis:8.4` image, which
provides `redis-server` and `redis-cli`, with Linux binaries built on the
host:

```sh
mkdir -p /tmp/dc-linux-bin
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/dc-linux-bin/ ./cmd/...
docker run --rm -v "$PWD":/work -v /tmp/dc-linux-bin:/work/bin -w /work \
  -e BENCH_PREBUILT=1 -e BENCH_GO_VERSION=go1.27.3 -e BENCH_HOST=docker-linux-arm64-m1 \
  redis:8.4 bash scripts/bench.sh
docker run --rm -v "$PWD":/work -v /tmp/dc-linux-bin:/work/bin -w /work \
  redis:8.4 bash scripts/bench-slow-command.sh
```

Keep the machine idle while it runs: a build or a test run on the host
during a cell changes that cell's numbers by tens of percent.

## Environment variables for `scripts/bench.sh`

- `BENCH_N`: requests per test (default 1000000).
- `BENCH_PREBUILT=1`: skip `go build` and use the existing `bin/` binaries,
  for example inside a container without Go. `BENCH_GO_VERSION` then names
  the toolchain that built them.
- `BENCH_HOST`: name used in the result file instead of `hostname -s`
  (containers have meaningless hostnames).
- `BENCH_NOTE`: free text recorded in the results header describing the setup.
- `BENCH_REDIS_ARGS`: extra `redis-server` arguments, for example
  `--io-threads 4`.

Before each matrix cell the script runs `FLUSHALL` on both servers so no cell
inherits data structures grown by earlier cells. It refuses to start if
something already answers on port 6390 or 6391.

## Variance caveat

Each cell is a single run with no repeats, so there is no variance estimate;
treat differences below roughly 10-15% as noise. Cells with 500 clients and
pipeline 16 are saturated (p99 in the hundreds of milliseconds) and noisier
still. When the load tool and both servers share one machine, or one Docker VM,
they compete for CPU, and Redis's single-threaded command execution is paired
with a multi-threaded load generator. The first baseline
`2026-11-05-docker-linux-arm64-m1.md` was produced in a Docker Desktop VM and
predates the per-cell flush, so its later cells ran on larger data structures;
the 2026-11-23 files replace it for comparisons. Compare results only from the
same machine and setup.

Latency percentiles use the nearest-rank rule (since commit 584c564; the 2026-11-05 baseline used int(q*n), one rank lower — negligible at ~100k samples).
