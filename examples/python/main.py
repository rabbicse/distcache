"""Short tour of common Redis features against DistCache using redis-py.

Prints "OK <step>" per check and exits non-zero on the first failure.
"""
import os
import sys
import time

import redis

host, _, port = os.environ.get("DISTCACHE_ADDR", "localhost:6379").partition(":")
r = redis.Redis(host=host, port=int(port), decode_responses=True)


def check(step, ok):
    if not ok:
        print(f"FAIL {step}", file=sys.stderr)
        sys.exit(1)
    print("OK", step)


check("ping", r.ping())

r.delete("ex:str", "ex:hash", "ex:list", "ex:set", "ex:zset", "ex:counter")
r.set("ex:str", "hello", ex=10)
check("string+ttl", r.get("ex:str") == "hello" and r.ttl("ex:str") > 0)

r.hset("ex:hash", mapping={"name": "ann", "age": "30"})
r.rpush("ex:list", "a", "b", "c")
r.sadd("ex:set", "x", "y", "x")
r.zadd("ex:zset", {"bob": 2, "amy": 1})
check("data types",
      r.hget("ex:hash", "name") == "ann"
      and r.lrange("ex:list", 0, -1) == ["a", "b", "c"]
      and r.scard("ex:set") == 2
      and r.zrange("ex:zset", 0, -1) == ["amy", "bob"])

pipe = r.pipeline(transaction=False)
for _ in range(100):
    pipe.incr("ex:counter")
pipe.execute()
check("pipeline", r.get("ex:counter") == "100")

tx = r.pipeline(transaction=True)
tx.incr("ex:counter").incr("ex:counter")
check("multi/exec", tx.execute() == [101, 102])

check("lua", r.eval("return redis.call('INCRBY', KEYS[1], ARGV[1])", 1, "ex:counter", 8) == 110)

ps = r.pubsub()
ps.subscribe("ex:news")
ps.get_message(timeout=1)  # subscribe confirmation
r.publish("ex:news", "hi")
msg = None
deadline = time.time() + 2
while msg is None and time.time() < deadline:
    msg = ps.get_message(ignore_subscribe_messages=True, timeout=0.5)
check("pubsub", msg is not None and msg["data"] == "hi")
ps.close()
