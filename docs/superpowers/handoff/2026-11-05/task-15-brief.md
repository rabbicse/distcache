### Task 15: Load tool `cmd/distcache-bench` and `make bench`

**Files:**
- Create: `cmd/distcache-bench/main.go`, `cmd/distcache-bench/runner.go`, `cmd/distcache-bench/runner_test.go`, `bench/run.sh`, `bench/results/README.md`
- Modify: `Makefile` (`build` adds the bench binary; `bench` target runs `bench/run.sh`)

**Interfaces:**
- Produces CLI: `distcache-bench [-h host] [-p port] [-c clients=50] [-n requests=100000] [-P pipeline=1] [-d datasize=3] [-t tests=set,get,incr,lpush,rpush,lpop,rpop,sadd,hset,zadd] [-r keyspace=0] [-q] [--csv] [--duration 0s]`.
- Produces: `type Result struct { Test string; Requests int; Seconds float64; OpsPerSec float64; P50, P95, P99 time.Duration }` and `func Run(cfg Config) ([]Result, error)` in `runner.go` using `resp.AppendCommandStrings` and `resp.Reader` directly (no third-party client), one goroutine per client, `-P` commands per write, latency sampled per pipeline batch into a sorted slice (reservoir of 100 000) for percentiles.
- Output: human table by default; `--csv` emits `test,ops_per_sec,p50_us,p95_us,p99_us`.

- [ ] **Step 1: Write failing test** `TestRunAgainstInProcessServer` (package `main`): `testutil.StartServer`, `Run(Config{Addr: addr, Clients: 4, Requests: 2000, Pipeline: 8, Tests: []string{"set","get"}})` → two results with `Requests==2000` and `OpsPerSec>0`; `-r 1000` produces keys `key:<n>` with `n<1000` (assert via `DBSIZE ≤ 1000`).
- [ ] **Step 2: Run** `go test ./cmd/distcache-bench` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `runner.go` and `main.go` (flag parsing mirrors redis-benchmark names exactly so the same script drives both binaries).
- [ ] **Step 4: Write `bench/run.sh`**: builds `bin/distcache-server` and `bin/distcache-bench`; starts DistCache on port 6390 (`--save "" --appendonly no --dir $TMP`); runs the matrix `{-P 1, -P 16} × {-c 50, -c 500}` with `-n 1000000 -t set,get,incr,lpush,sadd,hset,zadd -r 100000 --csv`; if `redis-server` is on PATH, starts it on 6391 with the same flags and runs the identical matrix; writes `bench/results/$(date +%F)-$(hostname -s).md` with a markdown table per configuration (DistCache vs Redis columns, ratio column) plus `go version`, `uname -a`, `nproc`/`sysctl -n hw.ncpu`; kills both servers on exit (`trap`).
- [ ] **Step 5: Run** `make bench` — Expected: results file written; commit it as the baseline (this file is the number sub-project 2 must beat). `bench/results/README.md` explains the matrix and how to reproduce.
- [ ] **Step 6: Commit** — `git add cmd/distcache-bench bench Makefile && git commit -m "bench: add distcache-bench load tool and baseline results"`

---

