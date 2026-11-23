# Slow-command benchmark 2026-11-23

Written by `scripts/bench-slow-command.sh`. 50 clients run `SET` and `GET`
(keyspace 100000, 3-byte values) while one more client runs
`SINTERCARD 2 big1 big2` back to back; `big1` and `big2` hold 500,000
members each, and one run takes tens of milliseconds on either server.

```
setup: Docker Desktop linuxkit VM (8 vCPU, 7.7 GiB) on an Apple M1 MacBook Pro; both servers and the load tool share the VM's 8 vCPUs
DistCache: commit bc5f97d, go1.27.3 linux/arm64
Redis: 8.4.8, default configuration, persistence off
requests: 300000 per test without the background command, 30000 with it
```

Two runs, to show the spread.

## Run 1

| server | background | test | ops/s | p50 us | p99 us |
|---|---|---|---:|---:|---:|
| DistCache | none | set | 170403 | 224 | 1340 |
| DistCache | none | get | 186213 | 216 | 1011 |
| DistCache | slow command loop | set | 17959 | 206 | 3734 |
| DistCache | slow command loop | get | 185772 | 228 | 839 |
| Redis | none | set | 98091 | 426 | 1970 |
| Redis | none | get | 89551 | 454 | 2327 |
| Redis | slow command loop | set | 792 | 53976 | 220314 |
| Redis | slow command loop | get | 978 | 50967 | 122902 |

## Run 2

| server | background | test | ops/s | p50 us | p99 us |
|---|---|---|---:|---:|---:|
| DistCache | none | set | 147171 | 256 | 1433 |
| DistCache | none | get | 163835 | 226 | 1483 |
| DistCache | slow command loop | set | 22248 | 156 | 3673 |
| DistCache | slow command loop | get | 161162 | 241 | 1899 |
| Redis | none | set | 95542 | 387 | 2614 |
| Redis | none | get | 98846 | 410 | 2012 |
| Redis | slow command loop | set | 659 | 58942 | 374232 |
| Redis | slow command loop | get | 936 | 49859 | 125492 |
