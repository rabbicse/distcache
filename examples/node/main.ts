// Short tour of common Redis features against DistCache using ioredis.
// Prints "OK <step>" per check and exits non-zero on the first failure.
import Redis from "ioredis";

const [host, port] = (process.env.DISTCACHE_ADDR ?? "localhost:6379").split(":");
const r = new Redis({ host, port: Number(port) });

function check(step: string, ok: boolean): void {
  if (!ok) {
    console.error(`FAIL ${step}`);
    process.exit(1);
  }
  console.log("OK", step);
}

check("ping", (await r.ping()) === "PONG");

await r.del("ex:str", "ex:hash", "ex:list", "ex:set", "ex:zset", "ex:counter");
await r.set("ex:str", "hello", "EX", 10);
check("string+ttl", (await r.get("ex:str")) === "hello" && (await r.ttl("ex:str")) > 0);

await r.hset("ex:hash", { name: "ann", age: "30" });
await r.rpush("ex:list", "a", "b", "c");
await r.sadd("ex:set", "x", "y", "x");
await r.zadd("ex:zset", 2, "bob", 1, "amy");
check(
  "data types",
  (await r.hget("ex:hash", "name")) === "ann" &&
    JSON.stringify(await r.lrange("ex:list", 0, -1)) === '["a","b","c"]' &&
    (await r.scard("ex:set")) === 2 &&
    JSON.stringify(await r.zrange("ex:zset", 0, -1)) === '["amy","bob"]',
);

const pipe = r.pipeline();
for (let i = 0; i < 100; i++) pipe.incr("ex:counter");
await pipe.exec();
check("pipeline", (await r.get("ex:counter")) === "100");

const tx = await r.multi().incr("ex:counter").incr("ex:counter").exec();
check("multi/exec", tx?.[1]?.[1] === 102);

check("lua", (await r.eval("return redis.call('INCRBY', KEYS[1], ARGV[1])", 1, "ex:counter", 8)) === 110);

const sub = new Redis({ host, port: Number(port) });
const got = new Promise<string>((resolve) => sub.on("message", (_ch, msg) => resolve(msg)));
await sub.subscribe("ex:news");
await r.publish("ex:news", "hi");
const msg = await Promise.race([got, new Promise<string>((res) => setTimeout(() => res(""), 2000))]);
check("pubsub", msg === "hi");

sub.disconnect();
r.disconnect();
