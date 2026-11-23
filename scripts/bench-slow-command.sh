#!/usr/bin/env bash
# Measures what a long-running command does to everyone else.
#
# 50 clients run SET and GET while one more client runs a long read-only
# command back to back (SINTERCARD over two 500,000-member sets). Redis
# executes commands on one thread, so the other clients wait for each run to
# finish. DistCache locks only the shards of the two sets, for reading.
#
# Needs redis-server and redis-cli on PATH and the binaries in bin/
# (make build). Inside the redis image, with Linux binaries mounted at
# /work/bin:
#   docker run --rm -v "$PWD":/work -v /path/to/linux/bin:/work/bin -w /work \
#     redis:8.4 bash scripts/bench-slow-command.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin"
DC_PORT=6390
RD_PORT=6391
TMP="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do
    if [ -n "$p" ]; then
      kill "$p" 2>/dev/null || true
      wait "$p" 2>/dev/null || true
    fi
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP/dc" "$TMP/rd"
"$BIN/distcache-server" --port "$DC_PORT" --save "" --appendonly no --dir "$TMP/dc" >"$TMP/dc.log" 2>&1 &
PIDS+=("$!")
redis-server --port "$RD_PORT" --save "" --appendonly no --dir "$TMP/rd" >"$TMP/rd.log" 2>&1 &
PIDS+=("$!")
sleep 2

for port in "$DC_PORT" "$RD_PORT"; do
  redis-cli -p "$port" eval "for i=1,500000 do redis.call('SADD','big1','member:'..i) redis.call('SADD','big2','member:'..i) end return 1" 0 >/dev/null
  "$BIN/distcache-bench" -p "$port" -c 50 -n 200000 -t set -r 100000 -q >/dev/null
done

run() { # port requests
  "$BIN/distcache-bench" -p "$1" -c 50 -n "$2" -t set,get -r 100000 --csv
}

echo "| server | background | test | ops/s | p50 us | p99 us |"
echo "|---|---|---|---:|---:|---:|"
for port in "$DC_PORT" "$RD_PORT"; do
  name=DistCache
  if [ "$port" = "$RD_PORT" ]; then name=Redis; fi
  run "$port" 300000 | awk -F, -v n="$name" '{printf "| %s | none | %s | %.0f | %s | %s |\n", n, $1, $2, $3, $5}'
  (yes "SINTERCARD 2 big1 big2" | redis-cli -p "$port" >/dev/null 2>&1) &
  loop=$!
  sleep 1
  run "$port" 30000 | awk -F, -v n="$name" '{printf "| %s | slow command loop | %s | %.0f | %s | %s |\n", n, $1, $2, $3, $5}'
  kill "$loop" 2>/dev/null || true
  pkill -f "redis-cli -p $port" 2>/dev/null || true
  wait "$loop" 2>/dev/null || true
  sleep 1
done
