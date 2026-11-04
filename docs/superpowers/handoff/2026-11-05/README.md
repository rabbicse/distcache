# Handoff: local → cloud (2026-11-05)

Resume the subagent-driven execution of
`docs/superpowers/plans/2026-11-04-test-harness-and-baseline.md`
(spec: `docs/superpowers/specs/2026-11-04-distcache-roadmap-and-test-harness-design.md`).

1. Copy `progress.md` and `go-conventions.md` from this folder into
   `.superpowers/sdd/2026-11-04-test-harness-and-baseline/` (git-ignored workspace).
   `progress.md` is the ledger: rulings R1–R9, bug list B1–B28, completed tasks.
2. Tasks 1–14 and the bug-fix batch are complete and reviewed.
3. Task 15 (cmd/distcache-bench, bench/run.sh, baseline) was interrupted mid-work.
   The partial work is in commit "wip(bench): Task 15 partial" — unreviewed. Finish it
   from `task-15-brief.md`, then review. On Linux you may install redis-server 8.4 /
   redis-benchmark (or use Docker redis:8.4) for the comparison and the differential tests
   (`REDIS_SERVER_ADDR`), which also settle candidates B20, B23 and sparse-HLL byte parity (B17).
4. Then Tasks 16–19, final whole-branch review, finishing-a-development-branch.
   Do not merge to master or open a PR without asking the user.
5. Next after this plan: sub-project 2 (throughput). New sub-project 7: React web
   console (RedisInsight-like, richer data views) — needs its own brainstorm/spec.

Delete this folder in the final cleanup commit.
