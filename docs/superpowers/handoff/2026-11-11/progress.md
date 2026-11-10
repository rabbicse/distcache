# SDD ledger — plan: docs/superpowers/plans/2026-11-10-web-console-phase-a.md
Spec: docs/superpowers/specs/2026-11-10-web-console-design.md. Branch web-console (from test-harness a3a2188).
Rules: author "Md Mehedi Hasan <rabbi.se@gmail.com>", no trailers; no push without asking.
Pre-flight scan: Task1 config fields → Task3-5,7,9 consume (names consistent). Task2 Stats() → Task7. Task5 session.db (*server.Session) → Task6,8 (Exec under session.mu). Task3 Options/Console → Task9. No conflicts. Note: server.Session authenticates as default user (exec.go) — fits phase A.
Task 1: impl 381c52c (base 72b6fe9).
Task 1 review: changes needed (2I: unvalidated trusted-proxy, timeout<=0; minors). Fix round 1 dispatched. Parked: m4 cert/key cross-check → Task 4/9; m5 rewrite drops empty (existing).
Task 1: fix e016497 verified by controller (tests pass). Task 1: complete — 381c52c, e016497.
Plan amended (user 2026-11-10): Task 6 + samples/seed/autocomplete/keyboard; new Task 8a pub/sub page (moved from phase B).
Task 2: impl ffb7e06 (base e016497/a2e6214). Bench within 3%. Decisions: lockall/MULTI/script ops not per-shard counted.
Task 2: complete — ffb7e06 + bb8b3e8 (minors by controller) — approved.
Plan amended: Task 7 maxmemory/policy control (user yes).
Task 3: impl a02b812. Notes: sse.js URL fix; htmx-config meta for CSP; /logout GET placeholder → Task 5 POST; Start binds port 0 → Task 9 must skip when WebPort==0.
Task 3 review: Approved (low only). Fix round 1 for m1 ReadTimeout/MaxHeaderBytes, m2 static dir listing, m5 tests. m4 logout→Task 5 (carry). Parked m3 (panic after partial write mixes body).
Task 3: complete — a02b812, 0baf038 — approved.
Task 4: impl 3d6913d. Proxy mode = web-tls off + trusted proxy per spec (auto+proxy still HTTPS) — ruling: follow spec — cost: none.
Task 4 review: Approved. Ruling M1: banner/warn only when plain HTTP actually served non-loopback — flag is a no-op otherwise — spec updated — cost: none. Fix round for L1-L3. Parked L4 (LOCALHOST/zone not loopback, fails safe), L5.
Task 4: complete — 3d6913d, c6fa00a — approved.
Task 5: impl b0ee492. Login POST: dc_login double-submit cookie (Origin null due to no-referrer). Task 9 must handle ErrNoPassword.
Task 5 review: changes needed (I1 lockout TOCTOU parallel guesses; I2 per-IPv6 lockout bypass). Rulings: I2 → /64 keying + global 100 fails/min limit — spec silent, standard practice — cost: legit users share /64 bucket. M2 __Host- prefix when Secure; M3 scheme+host, proxy headers; M4 XFF ports. Parked M1 (insecure no-pw eviction), M5 (spec keeps sessions on pw change), M6.
Task 5: fix 2e882c1 (I1 atomic reservation, I2 /64 + global limit, M2-M4) verified. Task 5: complete — b0ee492, 2e882c1.
master: merged test-harness → a4f22b2, pushed (user: "finally merge to master").
Task 6: impl 932aebd. Console sessions HELLO 3. B42: COMMAND DOCS returns only summary URL (no arguments/syntax) vs Redis 8.4 — hints weak until fixed. console.js not browser-tested (Task 9 Playwright).
Task 6 review: changes needed (2 High: XREADGROUP deny bypass hangs session; GETKEYS via Exec corrupts MULTI). Rulings: CommandKeyCount helper; 30s blocking cap; MULTI indicator; deny HELLO/AUTH/CLIENT TRACKING/SYNC/PSYNC/REPLCONF/SHUTDOWN/DEBUG (phase A safety — admin can use redis-cli — cost: less power in console); redact secrets in history; audit canonical name. Parked: m8 seed non-atomic, m10 unused data-hint.
Task 6 fix1 6136b90 re-review: double BLOCK bypass remains; PRE-EXISTING B43: xreadKeys mis-parses 'GROUP streams c' → wrong shard locks (unlocked access). Fix round 2 dispatched (shared XREAD option parser).
Task 6: fix2 b17405a (shared scanXReadOptions; B43 fixed; last BLOCK; redaction) verified. Task 6: complete — 932aebd, 6136b90, b17405a.
Task 7: impl 52f980b. B44: INFO lacks latencystats section (Redis 7+/8.4 has it) → p50/p99 tiles show —.
Task 7 review: changes needed (M1 SSE no write deadline → leak; M2 per-client render, no cap). Fix round 1: all M + lows incl. a11y. Parked: idle sampler leak if Console never closed (documented).
Task 7: fix 29ccf03 verified. Note: idle timeout (30m) ends dashboard streams — passive watching does not keep session alive (security > UX; user can raise web-session-timeout). Task 7: complete — 52f980b, 29ccf03.
