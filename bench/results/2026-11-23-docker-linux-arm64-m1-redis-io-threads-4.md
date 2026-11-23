# DistCache benchmark 2026-11-23

```
go:   go1.27.3 (prebuilt binaries)
uname: Linux b8647b573537 7.0.14-linuxkit #1 SMP PREEMPT Fri Oct 16 11:55:50 UTC 2026 aarch64 GNU/Linux
cpus: 8
cpu:  aarch64 (model not exposed; see note)
mem:  7.7 GiB
note: Docker Desktop linuxkit VM (8 vCPU, 7.7 GiB) on an Apple M1 MacBook Pro (8 cores, 16 GiB); both servers and the load tool share the VM's 8 vCPUs; DistCache at commit bc5f97d; Redis started with --io-threads 4
```

Requests per test: 1000000, tests: set,get,incr,lpush,sadd,hset,zadd, keyspace 100000, data size 3 bytes.
Redis: Redis server v=8.4.8 sha=00000000:1 malloc=jemalloc-5.3.0 bits=64 build=741beb89eacc971b
Redis arguments: --save "" --appendonly no --io-threads 4

## pipeline 1, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 248710 | 181555 | 1.37 | 151 | 939 |
| get | 275306 | 184114 | 1.50 | 138 | 790 |
| incr | 258978 | 181652 | 1.43 | 144 | 855 |
| lpush | 243986 | 159694 | 1.53 | 147 | 1021 |
| sadd | 205608 | 143629 | 1.43 | 173 | 1156 |
| hset | 181871 | 173824 | 1.05 | 202 | 1332 |
| zadd | 60145 | 147234 | 0.41 | 999 | 2347 |

## pipeline 1, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 287245 | 373480 | 0.77 | 1379 | 10033 |
| get | 265161 | 385366 | 0.69 | 1604 | 10413 |
| incr | 257789 | 362134 | 0.71 | 1635 | 8698 |
| lpush | 260353 | 358284 | 0.73 | 1328 | 8726 |
| sadd | 239951 | 369263 | 0.65 | 1555 | 8621 |
| hset | 234562 | 370809 | 0.63 | 1591 | 8663 |
| zadd | 77647 | 242141 | 0.32 | 6547 | 19966 |

## pipeline 16, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 799513 | 1265454 | 0.63 | 782 | 4867 |
| get | 987523 | 1388947 | 0.71 | 648 | 4101 |
| incr | 886133 | 1333376 | 0.66 | 711 | 4319 |
| lpush | 448498 | 595707 | 0.75 | 1153 | 8718 |
| sadd | 370624 | 1212631 | 0.31 | 1377 | 10442 |
| hset | 354560 | 1186702 | 0.30 | 1389 | 11268 |
| zadd | 92067 | 343946 | 0.27 | 4401 | 39330 |

## pipeline 16, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 984147 | 1689424 | 0.58 | 4999 | 43832 |
| get | 1237249 | 2218559 | 0.56 | 4288 | 38369 |
| incr | 907206 | 1865500 | 0.49 | 5676 | 42373 |
| lpush | 328295 | 590443 | 0.56 | 17276 | 110736 |
| sadd | 290405 | 1358175 | 0.21 | 19814 | 124008 |
| hset | 324278 | 1562041 | 0.21 | 13495 | 120946 |
| zadd | 95496 | 333411 | 0.29 | 54921 | 318711 |
