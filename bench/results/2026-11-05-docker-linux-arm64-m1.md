# DistCache benchmark 2026-11-05

```
go:   go1.27.3 linux/arm64 (prebuilt binaries)
uname: Linux ab5f82f52e85 7.0.14-linuxkit #1 SMP PREEMPT Fri Oct 16 11:55:50 UTC 2026 aarch64 GNU/Linux
cpus: 8
cpu:  aarch64 (model not exposed; see note)
mem:  7.7 GiB
note: Docker Desktop linuxkit VM (8 vCPU, 7.7 GiB) on an Apple M1 MacBook Pro (8 cores, 16 GiB); both servers and the load tool share the VM's 8 vCPUs
```

Requests per test: 1000000, tests: set,get,incr,lpush,sadd,hset,zadd, keyspace 100000, data size 3 bytes.
Redis: Redis server v=8.4.8 sha=00000000:1 malloc=jemalloc-5.3.0 bits=64 build=741beb89eacc971b

## pipeline 1, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 257778 | 195013 | 1.32 | 146 | 837 |
| get | 287842 | 207944 | 1.38 | 130 | 733 |
| incr | 263555 | 204775 | 1.29 | 143 | 768 |
| lpush | 254578 | 167802 | 1.52 | 139 | 936 |
| sadd | 234739 | 197355 | 1.19 | 153 | 1036 |
| hset | 231199 | 179709 | 1.29 | 153 | 1056 |
| zadd | 63428 | 127636 | 0.50 | 939 | 2330 |

## pipeline 1, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 286534 | 192808 | 1.49 | 1384 | 7458 |
| get | 306733 | 196034 | 1.56 | 1334 | 6180 |
| incr | 292771 | 194708 | 1.50 | 1422 | 6190 |
| lpush | 263902 | 165087 | 1.60 | 1298 | 8454 |
| sadd | 237125 | 193609 | 1.22 | 1522 | 8904 |
| hset | 216387 | 188069 | 1.15 | 1664 | 9970 |
| zadd | 64301 | 102167 | 0.63 | 7702 | 24742 |

## pipeline 16, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 938045 | 693284 | 1.35 | 570 | 4761 |
| get | 1066205 | 431297 | 2.47 | 495 | 4310 |
| incr | 946704 | 601876 | 1.57 | 622 | 4182 |
| lpush | 428408 | 222863 | 1.92 | 1173 | 9616 |
| sadd | 370677 | 759410 | 0.49 | 1370 | 10680 |
| hset | 349596 | 848815 | 0.41 | 1366 | 11967 |
| zadd | 68294 | 139674 | 0.49 | 9320 | 37587 |

## pipeline 16, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 422016 | 936714 | 0.45 | 10775 | 205128 |
| get | 694394 | 971513 | 0.71 | 7964 | 67410 |
| incr | 820699 | 985050 | 0.83 | 7602 | 38462 |
| lpush | 350172 | 464541 | 0.75 | 16406 | 105790 |
| sadd | 364798 | 783724 | 0.47 | 15629 | 96943 |
| hset | 265424 | 855428 | 0.31 | 15501 | 172794 |
| zadd | 63803 | 172509 | 0.37 | 132902 | 324908 |
