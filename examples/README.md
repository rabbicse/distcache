# DistCache client examples

DistCache speaks the Redis protocol, so standard Redis clients work unchanged.
Each folder holds one small program that runs the same checks against a server
on `localhost:6379` (override with `DISTCACHE_ADDR=host:port`):

1. PING and server info
2. Strings with TTL
3. Hash, list, set, sorted set
4. Pipeline (100 INCRs in one round trip)
5. MULTI/EXEC transaction
6. Lua script (EVAL)
7. Pub/Sub round trip

Each prints `OK <step>` per check and exits non-zero on the first failure.

| Language | Client | Run |
|---|---|---|
| Go | go-redis v9 | `cd go && go run .` |
| Java | Jedis 5 | `cd java && mvn -q compile exec:java` |
| Python | redis-py 5 | `cd python && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt && .venv/bin/python main.py` |
| Node/TypeScript | ioredis 5 | `cd node && npm install && npm start` |

Start a server first: `make build && ./bin/distcache-server`.
