package dev.distcache.example;

import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import redis.clients.jedis.Jedis;
import redis.clients.jedis.JedisPubSub;
import redis.clients.jedis.Pipeline;
import redis.clients.jedis.Transaction;

/** Short tour of common Redis features against DistCache using Jedis. */
public final class Main {
  private static void check(String step, boolean ok) {
    if (!ok) {
      System.err.println("FAIL " + step);
      System.exit(1);
    }
    System.out.println("OK " + step);
  }

  public static void main(String[] args) throws Exception {
    String[] addr = System.getenv().getOrDefault("DISTCACHE_ADDR", "localhost:6379").split(":");
    String host = addr[0];
    int port = Integer.parseInt(addr[1]);
    try (Jedis r = new Jedis(host, port)) {
      check("ping", "PONG".equals(r.ping()));

      r.del("ex:str", "ex:hash", "ex:list", "ex:set", "ex:zset", "ex:counter");
      r.setex("ex:str", 10, "hello");
      check("string+ttl", "hello".equals(r.get("ex:str")) && r.ttl("ex:str") > 0);

      r.hset("ex:hash", Map.of("name", "ann", "age", "30"));
      r.rpush("ex:list", "a", "b", "c");
      r.sadd("ex:set", "x", "y", "x");
      r.zadd("ex:zset", Map.of("bob", 2.0, "amy", 1.0));
      check("data types",
          "ann".equals(r.hget("ex:hash", "name"))
              && List.of("a", "b", "c").equals(r.lrange("ex:list", 0, -1))
              && r.scard("ex:set") == 2
              && List.of("amy", "bob").equals(r.zrange("ex:zset", 0, -1)));

      Pipeline p = r.pipelined();
      for (int i = 0; i < 100; i++) p.incr("ex:counter");
      p.sync();
      check("pipeline", "100".equals(r.get("ex:counter")));

      Transaction tx = r.multi();
      tx.incr("ex:counter");
      tx.incr("ex:counter");
      List<Object> res = tx.exec();
      check("multi/exec", Long.valueOf(102).equals(res.get(1)));

      Object v = r.eval("return redis.call('INCRBY', KEYS[1], ARGV[1])", List.of("ex:counter"), List.of("8"));
      check("lua", Long.valueOf(110).equals(v));

      CountDownLatch ready = new CountDownLatch(1);
      CountDownLatch got = new CountDownLatch(1);
      AtomicReference<String> msg = new AtomicReference<>();
      JedisPubSub listener = new JedisPubSub() {
        @Override public void onSubscribe(String ch, int n) { ready.countDown(); }
        @Override public void onMessage(String ch, String m) { msg.set(m); got.countDown(); unsubscribe(); }
      };
      Thread t = new Thread(() -> { try (Jedis s = new Jedis(host, port)) { s.subscribe(listener, "ex:news"); } });
      t.start();
      ready.await(2, TimeUnit.SECONDS);
      r.publish("ex:news", "hi");
      check("pubsub", got.await(2, TimeUnit.SECONDS) && "hi".equals(msg.get()));
      t.join(2000);
    }
  }
}
