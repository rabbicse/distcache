#!/usr/bin/env bash
# Runs the load matrix against DistCache (and Redis, if redis-server is on
# PATH) and writes bench/results/<date>-<host>.md. Override the request count
# with BENCH_N for a quicker run. Set BENCH_PREBUILT=1 to use existing bin/
# binaries (for example inside a container without Go; BENCH_GO_VERSION then
# names the toolchain that built them), BENCH_HOST to name the result file
# when the hostname is meaningless (containers) and BENCH_NOTE to record how
# the run was set up.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
N="${BENCH_N:-1000000}"
TESTS="set,get,incr,lpush,sadd,hset,zadd"
DC_PORT=6390
RD_PORT=6391
TMP="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p bin bench/results
if [ "${BENCH_PREBUILT:-0}" != 1 ]; then
  go build -o bin/distcache-server ./cmd/distcache-server
  go build -o bin/distcache-bench ./cmd/distcache-bench
fi
GOVER="$(go version 2>/dev/null || echo "${BENCH_GO_VERSION:-unknown} (prebuilt binaries)")"

wait_port() { # port
  for _ in $(seq 1 100); do
    if bin/distcache-bench -p "$1" -c 1 -n 1 -t ping -q >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  echo "server on port $1 did not start" >&2
  return 1
}

mkdir -p "$TMP/dc"
bin/distcache-server --port "$DC_PORT" --save "" --appendonly no --dir "$TMP/dc" >"$TMP/dc.log" 2>&1 &
PIDS+=($!)
wait_port "$DC_PORT"

HAVE_REDIS=0
if command -v redis-server >/dev/null 2>&1; then
  HAVE_REDIS=1
  mkdir -p "$TMP/rd"
  redis-server --port "$RD_PORT" --save "" --appendonly no --dir "$TMP/rd" >"$TMP/rd.log" 2>&1 &
  PIDS+=($!)
  wait_port "$RD_PORT"
fi

NCPU="$(nproc 2>/dev/null || sysctl -n hw.ncpu)"
OUT="bench/results/$(date +%F)-${BENCH_HOST:-$(hostname -s)}.md"
{
  echo "# DistCache benchmark $(date +%F)"
  echo
  echo '```'
  echo "go:   $GOVER"
  echo "uname: $(uname -a)"
  echo "cpus: $NCPU"
  if [ "$(uname)" = Darwin ]; then
    echo "cpu:  $(sysctl -n machdep.cpu.brand_string)"
    echo "mem:  $(( $(sysctl -n hw.memsize) / 1073741824 )) GiB"
  elif [ -r /proc/meminfo ]; then
    model="$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//' || true)"
    echo "cpu:  ${model:-$(uname -m) (model not exposed; see note)}"
    echo "mem:  $(awk '/MemTotal/ {printf "%.1f GiB", $2/1048576}' /proc/meminfo)"
  fi
  if [ -n "${BENCH_NOTE:-}" ]; then echo "note: $BENCH_NOTE"; fi
  echo '```'
  echo
  echo "Requests per test: $N, tests: $TESTS, keyspace 100000, data size 3 bytes."
  if [ "$HAVE_REDIS" = 1 ]; then
    echo "Redis: $(redis-server --version)"
  else
    echo "Redis comparison: pending (redis-server not on PATH when this ran)."
  fi
} >"$OUT"

run() { # port clients pipeline
  bin/distcache-bench -p "$1" -c "$2" -P "$3" -n "$N" -t "$TESTS" -r 100000 --csv
}

for P in 1 16; do
  for C in 50 500; do
    echo "== -P $P -c $C" >&2
    run "$DC_PORT" "$C" "$P" >"$TMP/dc.csv"
    if [ "$HAVE_REDIS" = 1 ]; then
      run "$RD_PORT" "$C" "$P" >"$TMP/rd.csv"
    else
      : >"$TMP/rd.csv"
    fi
    {
      echo
      echo "## pipeline $P, clients $C"
      echo
      echo "| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |"
      echo "|---|---:|---:|---:|---:|---:|"
      awk -F, '
        FILENAME == ARGV[1] { rd[$1]=$2; next }
        { r = ($1 in rd) ? sprintf("%.0f", rd[$1]) : "n/a"
          ratio = ($1 in rd) ? sprintf("%.2f", $2/rd[$1]) : "n/a"
          printf "| %s | %.0f | %s | %s | %s | %s |\n", $1, $2, r, ratio, $3, $5 }
      ' "$TMP/rd.csv" "$TMP/dc.csv"
    } >>"$OUT"
  done
done
echo "wrote $OUT"
