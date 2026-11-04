# DistCache benchmark 2026-11-05

```
go:   go version go1.27.3 darwin/arm64
uname: Darwin Mds-MacBook-Pro.local 25.6.0 Darwin Kernel Version 25.6.0: Fri Jul 31 19:17:12 PDT 2026; root:xnu-12377.161.14~5/RELEASE_ARM64_T8103 arm64
cpus: 8
cpu:  Apple M1
mem:  16 GiB
```

Requests per test: 1000000, tests: set,get,incr,lpush,sadd,hset,zadd, keyspace 100000, data size 3 bytes.
Redis comparison: pending (redis-server not on PATH when this ran).
