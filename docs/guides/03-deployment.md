# Guide 3 — Installing and deploying DistCache

DistCache is one static binary with no dependencies. This guide covers the
ways to run it: a release archive on Linux, macOS or Windows, a container, a
Compose stack, and Kubernetes. It ends with a checklist for production.

| Target | What you use | Status |
|---|---|---|
| Linux (amd64, arm64) | `distcache-<version>-linux-<arch>.tar.gz` | tested in CI |
| macOS (Intel, Apple silicon) | `distcache-<version>-darwin-<arch>.tar.gz` | tested in CI |
| Windows (amd64, arm64) | `distcache-<version>-windows-<arch>.zip` | preview: it builds and is expected to work, but the test suite does not gate on Windows yet |
| Docker | `deploy/docker/Dockerfile`, `docker-compose.yml` | built and smoke-tested in CI |
| Kubernetes | `deploy/kubernetes` (kustomize) | manifests render in CI; apply them to a test cluster before production |

## 1. Get the binaries

Tagged releases attach the archives and a `SHA256SUMS` file to the GitHub
release. To build them yourself you need only Go 1.27:

```sh
make dist              # all six archives, in dist/
TARGETS="linux/amd64" make dist
```

Each archive holds `distcache-server`, `distcache-bench` (the load tool), a
commented `distcache.conf`, and on Linux a systemd unit.

Check a download before you run it:

```sh
sha256sum -c SHA256SUMS --ignore-missing      # macOS: shasum -a 256 -c SHA256SUMS --ignore-missing
```

## 2. Settings that matter everywhere

DistCache reads a `redis.conf`-style file and `--name value` arguments;
arguments win. `deploy/distcache.conf` documents each setting.

| Setting | Default | Why you care |
|---|---|---|
| `bind`, `port` | `0.0.0.0`, `6379` | Where clients connect. |
| `requirepass` | unset | The password. Without one, protected mode serves only clients on the same machine. |
| `protected-mode` | `yes` | Leave it on unless the network itself is the access control (a container port, a private subnet). |
| `dir` | `.` | Where `dump.dcs` and `appendonly.aof` live. Put it on a persistent disk. |
| `appendonly`, `appendfsync` | `no`, `everysec` | Turn `appendonly` on to keep every acknowledged write. `everysec` can lose up to one second on a crash; `always` loses nothing and is slower. |
| `save` | `3600 1 300 100 60 10000` | Automatic snapshots. They are also what a server without the AOF restarts from. |
| `maxmemory`, `maxmemory-policy` | `0`, `noeviction` | Cap the data set, for example `4gb`. Leave room for client buffers and Go's own overhead: about 60% of the machine or container limit is a safe start. |
| `shards` | `256` | Number of keyspace shards. Fixed at startup; the default suits up to a few dozen cores. |

On `SIGTERM` or Ctrl-C the server stops taking commands, writes its data and
exits. Give it time to finish: a large data set can take tens of seconds.

## 3. Linux

```sh
tar -xzf distcache-1.0.0-linux-amd64.tar.gz && cd distcache-1.0.0-linux-amd64
./distcache-server distcache.conf            # foreground, Ctrl-C to stop
```

As a service:

```sh
sudo useradd --system --home /var/lib/distcache --shell /usr/sbin/nologin distcache
sudo install -m 0755 distcache-server /usr/local/bin/
sudo install -d -o distcache -g distcache -m 0750 /var/lib/distcache
sudo install -D -m 0640 -o root -g distcache distcache.conf /etc/distcache/distcache.conf
sudo sed -i 's|^dir .*|dir /var/lib/distcache|' /etc/distcache/distcache.conf
sudo install -m 0644 distcache.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now distcache
journalctl -u distcache -f
```

Set `requirepass` and `appendonly yes` in `/etc/distcache/distcache.conf`
before you start it. The unit runs as an unprivileged user that can write
only to `/var/lib/distcache`.

## 4. macOS

```sh
tar -xzf distcache-1.0.0-darwin-arm64.tar.gz && cd distcache-1.0.0-darwin-arm64
xattr -d com.apple.quarantine distcache-server distcache-bench 2>/dev/null   # downloaded binaries are not notarized
./distcache-server distcache.conf
```

## 5. Windows (preview)

Unzip the archive, then in PowerShell:

```powershell
cd distcache-1.0.0-windows-amd64
.\distcache-server.exe distcache.conf --dir C:\distcache-data
```

Create the data folder first. Stop the server with Ctrl-C so it can save.
Unix sockets and the systemd unit do not apply. To run it as a service, wrap
it with a service manager such as NSSM or `sc.exe`. If you need a supported
setup on a Windows machine today, run the Linux container under Docker
Desktop or WSL 2 instead.

## 6. Docker

```sh
make docker                                   # builds distcache:dev
docker run -d --name distcache -p 127.0.0.1:6379:6379 -v distcache-data:/data \
  distcache:dev --dir /data --appendonly yes --requirepass "$(openssl rand -base64 32)"
```

The image is about 7 MB: the static binary on a distroless base, running as
uid 65532 with `/data` as its only writable path. It has no shell and no
client; test it from the host or from another container.

Arguments after the image name replace the default command, so pass `--dir
/data` yourself, as above. With no arguments the container runs with
`--protected-mode no`, because every client of a container arrives from a
non-loopback address. That is fine while the port is published only on
`127.0.0.1`. Set a password before you publish it more widely.

For several CPU architectures in one image:

```sh
make docker-multiarch IMAGE=registry.example.com/distcache:1.0.0 DOCKER_FLAGS=--push
```

Pushing a tag such as `v1.0.0` to GitHub runs `.github/workflows/release.yml`,
which publishes `ghcr.io/rabbicse/distcache:<version>` for amd64 and arm64.

### Compose

```sh
export DISTCACHE_PASSWORD="$(openssl rand -base64 32)"
docker compose -f deploy/docker/docker-compose.yml up -d                      # one node on 127.0.0.1:6379
docker compose -f deploy/docker/docker-compose.yml --profile replica up -d    # plus a read-only replica on :6380
```

Both services keep their data in named volumes, run with a read-only root
filesystem and get two minutes to shut down cleanly.

## 7. Kubernetes

`deploy/kubernetes/base` is one DistCache pod in a StatefulSet with a 10 Gi
volume, a ClusterIP Service `distcache:6379`, and its configuration in a
ConfigMap.

```sh
kubectl create namespace distcache
kubectl -n distcache create secret generic distcache-auth \
  --from-literal=password="$(openssl rand -base64 32)"
kubectl apply -k deploy/kubernetes/base
kubectl -n distcache rollout status statefulset/distcache
```

The pod will not start until the `distcache-auth` Secret exists. That is on
purpose: there is no way to deploy it without a password.

Before you apply it, set three things:

1. **The image.** `kustomization.yaml` points at
   `ghcr.io/rabbicse/distcache:1.0.0`. Change `newName` and `newTag` to an
   image you have pushed.
2. **Memory.** The container limit is 2 Gi, `GOMEMLIMIT` is 1800 MiB and
   `maxmemory` is 1 GB. Raise all three together and keep the same
   proportions.
3. **Storage.** `volumeClaimTemplates` asks for 10 Gi from the default
   storage class.

Try it from inside the cluster:

```sh
kubectl -n distcache run cli --rm -it --image=redis:8.4 -- \
  redis-cli -h distcache -a "$(kubectl -n distcache get secret distcache-auth -o jsonpath='{.data.password}' | base64 -d)" ping
```

### Primary and replicas

```sh
kubectl apply -k deploy/kubernetes/overlays/replication
```

This adds a second StatefulSet with two read-only replicas and a Service
`distcache-replica:6379` for reads. Writes still go to `distcache:6379`.

Replication is asynchronous and DistCache has **no automatic failover**. If
the primary pod dies, Kubernetes restarts it on the same volume and it
reloads its own data; the replicas reconnect by themselves. Clients cannot
write during that time. A replica is not promoted for you. If you need a
primary that survives the loss of a node without waiting for the restart,
DistCache is not ready for that yet (cluster mode is planned).

Not in the manifests, and worth adding for your cluster: a NetworkPolicy
that admits only your application's pods, a PodDisruptionBudget, and backups
of the volume.

## 8. Checklist before production

- [ ] `requirepass` is set to a long random value, and the port is reachable
      only from the hosts that need it. DistCache has no TLS: use a private
      network or a TLS-terminating proxy.
- [ ] `appendonly yes`, on a persistent disk. Schedule `BGREWRITEAOF` (for
      example nightly): the log is not compacted automatically.
- [ ] `maxmemory` is set, with a policy that fits: `noeviction` for a
      database, `allkeys-lru` or `allkeys-lfu` for a cache.
- [ ] `enable-debug-command` and `enable-protected-configs` are left at `no`.
- [ ] You back up `dump.dcs` and `appendonly.aof`. A copy of the data
      directory taken after `BGSAVE` is a consistent backup.
- [ ] You have restarted the server on a copy of production data and checked
      that it comes back with the same `DBSIZE`.
- [ ] You know what is not there: failover, cluster mode, ACL users and TLS.
      [redis-compatibility.md](../redis-compatibility.md) has the full list.
