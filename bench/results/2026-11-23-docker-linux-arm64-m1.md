# DistCache benchmark 2026-11-23

```
go:   go1.27.3 (prebuilt binaries)
uname: Linux 5a33e6c42787 7.0.14-linuxkit #1 SMP PREEMPT Fri Oct 16 11:55:50 UTC 2026 aarch64 GNU/Linux
cpus: 8
cpu:  aarch64 (model not exposed; see note)
mem:  7.7 GiB
note: Docker Desktop linuxkit VM (8 vCPU, 7.7 GiB) on an Apple M1 MacBook Pro (8 cores, 16 GiB); both servers and the load tool share the VM's 8 vCPUs; DistCache at commit bc5f97d
```

Requests per test: 1000000, tests: set,get,incr,lpush,sadd,hset,zadd, keyspace 100000, data size 3 bytes.
Redis: Redis server v=8.4.8 sha=00000000:1 malloc=jemalloc-5.3.0 bits=64 build=741beb89eacc971b
Redis arguments: --save "" --appendonly no 

## pipeline 1, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 203386 | 158361 | 1.28 | 186 | 1011 |
| get | 181804 | 167837 | 1.08 | 204 | 1084 |
| incr | 161952 | 164341 | 0.99 | 216 | 1465 |
| lpush | 205886 | 141336 | 1.46 | 169 | 1145 |
| sadd | 201586 | 165063 | 1.22 | 173 | 1227 |
| hset | 191821 | 158188 | 1.21 | 181 | 1327 |
| zadd | 49572 | 108023 | 0.46 | 1030 | 10108 |

## pipeline 1, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 244847 | 185580 | 1.32 | 1708 | 11996 |
| get | 285322 | 192623 | 1.48 | 1432 | 8175 |
| incr | 269149 | 193050 | 1.39 | 1560 | 7981 |
| lpush | 228200 | 169415 | 1.35 | 1487 | 10783 |
| sadd | 184546 | 192309 | 0.96 | 1917 | 13431 |
| hset | 201071 | 190143 | 1.06 | 1856 | 10240 |
| zadd | 64944 | 115195 | 0.56 | 7607 | 25560 |

## pipeline 16, clients 50

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 798498 | 965309 | 0.83 | 763 | 5640 |
| get | 867758 | 1148316 | 0.76 | 757 | 4616 |
| incr | 900000 | 970813 | 0.93 | 651 | 4396 |
| lpush | 447634 | 445127 | 1.01 | 1144 | 9154 |
| sadd | 369983 | 782883 | 0.47 | 1381 | 10265 |
| hset | 304252 | 829461 | 0.37 | 1584 | 13848 |
| zadd | 101305 | 238661 | 0.42 | 3725 | 36869 |

## pipeline 16, clients 500

| test | DistCache ops/s | Redis ops/s | ratio | DistCache p50 us | p99 us |
|---|---:|---:|---:|---:|---:|
| set | 799668 | 921820 | 0.87 | 6268 | 52121 |
| get | 1148564 | 1080742 | 1.06 | 4675 | 34997 |
| incr | 829718 | 962520 | 0.86 | 6012 | 52196 |
| lpush | 400684 | 450853 | 0.89 | 13431 | 94094 |
| sadd | 333398 | 741135 | 0.45 | 16474 | 109403 |
| hset | 320920 | 821219 | 0.39 | 13554 | 124753 |
| zadd | 92698 | 280791 | 0.33 | 64133 | 313300 |
