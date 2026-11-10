# Resume here (paused 2026-11-11)

State: `master` = test-harness (sub-project 1) + web-console Tasks 1–7. Every task is reviewed and fixed, and the full `-race` and e2e suites are green.
The web console package is **not yet started by distcache-server** (that is Task 9), so the shipped binary behaves exactly as before.

To resume, say: *"Follow docs/superpowers/handoff/2026-11-11/README.md and continue the plan."*
1. Copy `progress.md` and `go-conventions.md` back to `.superpowers/sdd/2026-11-10-web-console-phase-a/` (the SDD ledger is gitignored).
2. Continue `docs/superpowers/plans/2026-11-10-web-console-phase-a.md`, subagent-driven, from Task 8.
3. Rules: commits are authored by `Md Mehedi Hasan <rabbi.se@gmail.com>` with no Co-Authored-By trailers. Ask before pushing. Use stdlib net/http, not gin.

## TODO

### Web console phase A (sub-project 7)
- [ ] Task 8: key browser and per-type viewers (SCAN paging, odd key names, huge values, escaping)
- [ ] Task 8a: Pub/Sub page (live SUBSCRIBE/PSUBSCRIBE over SSE, publish, limits)
- [ ] Task 9: start the console from distcache-server (handle `ErrNoPassword`, skip when `web-port 0`), e2e test, guide `docs/guides/02-web-console.md`, README/CLAUDE.md/architecture updates, Playwright check of every page (console.js has not been tested in a browser yet)
- [ ] Final whole-branch review, then delete this handoff folder

### Compatibility bugs found along the way
- [ ] B42 `COMMAND DOCS` returns only a summary URL, with no arguments/syntax (console autocomplete hints are weak)
- [ ] B44 `INFO` has no `latencystats` section (dashboard p50/p99 show "—")
- [ ] B26 FIFO fairness against non-blocked poppers
- [ ] B27 a replica restart forces a full resync (snapshot lacks repl id/offset)
- [ ] B28 DEBUG is not gated by `enable-debug-command` (security; sub-project 4)
- [ ] Parked minors from bug batch 2: PFCOUNT in a script never caches; no lazy expiry under exclusive locks; cacheHLLCount resets LRU/LFU

### Ops / housekeeping
- [ ] Re-run `make bench` with Docker + redis:8.4.8 (the baseline predates the per-cell FLUSHALL)
- [ ] Run the Java/Python/Node examples (they need Jedis / redis-py / ioredis installed)
- [ ] Install `gh` (`brew install gh && gh auth login`) so CI status can be checked from here
- [ ] Decide on the license (currently Apache-2.0; AGPL is the alternative for a server)
- [ ] Optional: rewrite old "Mehmet" commits (needs a force-push of master)
- [ ] Docker Hub publishing (OCI labels are already in deploy/docker/Dockerfile)

### Next sub-projects
- [ ] Web console phase B: ACL users/roles (needs part of sub-project 4), editing, server pages, MONITOR, value masking
- [ ] Sub-project 2: single-node throughput (remove the global propagator lock, sharded counters) plus a Unix socket listener
- [ ] Sub-project 3: Redis-Cluster-compatible cluster mode
- [ ] Sub-project 4: hardening (ACL, TLS on RESP, auto AOF rewrite, FAILOVER)
- [ ] Sub-project 5: WebSocket gateway for browsers, client examples polish
- [ ] Offer to turn go-conventions.md into a skill
