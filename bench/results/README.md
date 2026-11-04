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
