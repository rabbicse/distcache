# DistCache

A high‑performance **distributed in‑memory cache** written in **Go**.

DistCache is designed as a Redis‑compatible caching system that supports clustering, replication, and high throughput. The goal of the project is to demonstrate modern distributed systems design including networking, persistence, sharding, and observability.

---

# Features

* Redis compatible RESP protocol
* High performance TCP server
* Distributed cluster support
* Consistent hashing for sharding
* In‑memory key‑value storage
* TTL expiration engine
* Persistence (Append Only File / Snapshot)
* Pub/Sub messaging
* Replication (Master → Replica)
* Prometheus metrics
* Structured logging
* Graceful shutdown

---

# Architecture

```
                Clients
                   │
               TCP Server
                   │
               RESP Parser
                   │
              Command Router
                   │
             Execution Engine
                   │
              In‑Memory Store
                   │
      ┌────────────┴────────────┐
      │                         │
 Persistence (AOF/RDB)     Replication
      │                         │
   Storage                Replica Nodes
```

Cluster architecture:

```
               Load Balancer
                     │
           ┌─────────┴─────────┐
           │                   │
      ClusterCache Node    ClusterCache Node
           │                   │
      Local Memory Store  Local Memory Store
```

---

# Project Structure

```
clustercache
│
├── cmd/
│   └── server/
│
├── internal/
│   ├── server/
│   ├── protocol/
│   ├── command/
│   ├── storage/
│   ├── persistence/
│   ├── cluster/
│   └── metrics/
│
├── pkg/
│   └── logger/
│
├── configs/
│
├── Dockerfile
└── README.md
```

---

# Quick Start

## Requirements

* Go 1.22+
* Docker (optional)

## Run Locally

```
go run ./cmd/distcache-server
```

Server starts on:

```
localhost:6379
```

---

# Example Usage

Using redis-cli:

```
redis-cli -p 6379
```

Commands:

```
PING
SET name mehedi
GET name
DEL name
```

---

# Configuration

Example configuration file:

```
server:
  port: 6379

cluster:
  enabled: true
  node_id: node1

persistence:
  aof: true
  snapshot_interval: 300
```

---

# Metrics

ClusterCache exposes Prometheus metrics:

```
/metrics
```

Example metrics:

* commands_total
* connections_total
* memory_usage_bytes

---

# Roadmap

Planned features:

* Distributed cluster mode
* Consistent hashing
* Replication protocol
* Pub/Sub system
* Advanced eviction policies
* Stream processing
* Multi‑region replication

---

# Benchmark Goals

Target performance:

* 500k+ operations/sec per node
* sub‑millisecond latency

---

# Contributing

Contributions are welcome.

Steps:

1. Fork the repository
2. Create a feature branch
3. Submit a pull request

---

# License

MIT License

---

# Author

Mehedi Hasan

Distributed Systems & Backend Engineering
