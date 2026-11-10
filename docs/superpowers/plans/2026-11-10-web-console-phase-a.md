# Web Console Phase A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship phase A of the built-in web console: login, dashboard with shard heatmap, key browser with per-type viewers, and command console, served by `distcache-server` on `web-port`.

**Architecture:** A new package `internal/web` serves htmx pages rendered with `html/template`, with all assets embedded via `go:embed`. Each browser session runs real commands through an `*server.Session` (the existing in-process client in `internal/server/exec.go`), so the normal dispatcher checks apply. Metrics that RESP cannot express come from a new read-only `(*server.Server).Stats()` API backed by atomics.

**Tech Stack:** Go 1.27.3 stdlib only (`net/http`, `html/template`, `crypto/tls`, `crypto/x509`, `embed`), plus vendored htmx 2.0.4 and htmx-ext-sse 2.2.2 as static files.

**Spec:** docs/superpowers/specs/2026-11-10-web-console-design.md (phase A only: spec §§2–8 minus items marked "Phase B")

## Global Constraints

- No new Go module dependencies. htmx files are vendored under `internal/web/static/` with their version and source URL in a header comment; their licence (0BSD) is noted in `NOTICE`.
- RESP behaviour on the data port must not change, and the full existing suite (`go test -race ./...`, `test/e2e`) must stay green.
- A web console failure must never stop the RESP server. Bind, TLS and handler errors are logged; handler panics are recovered.
- The default port is 7380. `web-port` defaults to 0 (off). `web-bind` defaults to 127.0.0.1.
- Theme: background `#0a0a0c`, accent gradient `#22d3ee → #a78bfa`, monospace for data.
- Follow `.superpowers/sdd/2026-11-10-web-console-phase-a/go-conventions.md` (idiomatic names, doc comment on every exported identifier, file-header comment per file).
- Commits are authored by `Md Mehedi Hasan <rabbi.se@gmail.com>` with no Co-Authored-By or other trailers.
- Platforms: Linux and macOS.

## Review Focus

1. A browser tab left open past the session timeout: the next htmx request must get a redirect to /login (`HX-Redirect` header), not a broken fragment. → Task 5 test `TestAuth_ExpiredSessionHTMXRedirect`.
2. Huge values, such as a 5 MB string or a 1M-element hash: the viewer must page and truncate, never render everything. → Task 8 test `TestKeys_LargeValuesArePaged`.
3. A key name containing `/`, `?`, `#`, spaces, unicode or invalid UTF-8 must round-trip through URLs and open the right key. → Task 8 test `TestKeys_OddKeyNamesRoundTrip`.
4. Concurrent requests from one browser session, such as SSE plus a console command: `server.Session` is not safe for concurrent use, so it must be serialised. → Task 6 test `TestConsole_ConcurrentRequestsSameSession` (`-race`).
5. `requirepass` changed via CONFIG SET while sessions exist: existing sessions stay valid, and new logins use the new password. → Task 5 test `TestAuth_PasswordChangeAppliesToNewLogins`.

---

### Task 1: Configuration parameters

**Files:**
- Modify: `internal/config/config.go` (fields plus `param` registrations)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces fields on `config.Config`:
  - `WebPort int`, `WebBind string`, `WebTLS string`, `WebTLSCert string`, `WebTLSKey string`, `WebTLSHosts []string`, `WebTrustedProxy []string`, `WebInsecure bool`, `WebSessionTimeout time.Duration`.
  - All are static (immutable via CONFIG SET, like `port`).

- [ ] **Step 1: Write failing test** `TestConfig_WebParams`:
  - Defaults: `WebPort==0`, `WebBind=="127.0.0.1"`, `WebTLS=="auto"`, `WebSessionTimeout==30*time.Minute`, `WebInsecure==false`.
  - Parsing `--web-port 7380 --web-tls on --web-trusted-proxy 10.0.0.1,10.1.0.0/16 --web-session-timeout 45m --web-insecure yes` sets them.
  - `web-tls maybe` is an error.
  - `CONFIG GET web-port` returns "7380".
  - CONFIG SET of `web-port` is rejected as immutable, with the same error text other immutable params use.
  - The same values parse from a config-file line (`web-port 7380`).
- [ ] **Step 2: Run** `go test ./internal/config -run TestConfig_WebParams` — expect FAIL.
- [ ] **Step 3: Implement.**
  - Register the parameters with the existing `add`/`strParam` helpers.
  - The duration accepts Go syntax (`30m`) or plain seconds (`1800`).
  - The lists are comma-separated.
- [ ] **Step 4: Run** `go test -race ./internal/config` — expect PASS.
- [ ] **Step 5: Commit** `feat(config): web console parameters`.

### Task 2: Server stats API

**Files:**
- Create: `internal/server/stats_api.go`
- Modify: `internal/storage/keyspace.go` (per-shard counters), `internal/server/dispatch.go` (count ops per shard)
- Test: `internal/server/stats_api_test.go`, `internal/storage/keyspace_test.go`

**Interfaces:**
- Produces in package `server`:

  ```go
  type ShardStat struct{ Keys, Ops int64; LockWait time.Duration }
  type StatsSnapshot struct {
      Time time.Time; OpsPerSec, Commands, Clients, UsedMemory, MaxMemory,
      Hits, Misses, Evicted, Expired, NetIn, NetOut int64
      Role string // "master" | "replica"
      Shards []ShardStat
  }
  func (s *Server) Stats() StatsSnapshot
  ```

- Produces in package `storage`:
  - `func (ks *Keyspace) ShardStats(dst []storage.ShardCounters) []storage.ShardCounters`, with `ShardCounters{Keys, Ops int64; LockWaitNanos int64}`.

- [ ] **Step 1: Write failing tests.**
  - `TestKeyspace_ShardStatsCountsKeysAndOps`: after 100 SETs, `Keys` summed over shards is 100 and `Ops` summed is at least 100.
  - `TestKeyspace_LockWaitRecordedOnlyWhenContended`: an uncontended Lock leaves `LockWaitNanos` at 0. When a goroutine holds a shard's write lock for 20 ms and a second goroutine locks the same shard, `LockWaitNanos` for that shard is at least 15 ms.
  - `TestServer_StatsSnapshot`: `Stats()` returns `len(Shards)==cfg shards`, `Role=="master"`, `Clients>=1` while a test client is connected, and `Commands` increasing across calls.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.**
  - Shard locking: `Lock`/`LockShard` first try `TryLock`/`TryRLock`. Only on failure do they take `time.Now()`, block, then add the elapsed time to an `atomic.Int64` on the shard. The uncontended path gains no clock calls.
  - Ops are counted with an atomic add per locked shard in `call`.
  - Keys come from the shard's per-DB map lengths, read under `RLock` per shard one at a time, never all at once.
  - `Stats()` reads the existing `stats` atomics, the same sources as INFO.
- [ ] **Step 4: Run** `go test -race ./internal/storage ./internal/server`, and `go test -run xxx -bench 'Get|Set' ./internal/server` compared with `bench/results/microbench-baseline.txt`. Expect PASS, and the Get/Set ns/op regression must be within 3%.
- [ ] **Step 5: Commit** `feat(server): read-only stats API with per-shard counters`.

### Task 3: Web package skeleton, assets, middleware

**Files:**
- Create:
  - `internal/web/server.go`, `internal/web/middleware.go`
  - `internal/web/templates/layout.html`, `internal/web/templates/error.html`
  - `internal/web/static/{htmx.min.js,htmx-ext-sse.js,app.css,logo-mark.svg}`
- Modify: `NOTICE` (htmx 0BSD line)
- Test: `internal/web/server_test.go`

**Interfaces:**
- Produces:

  ```go
  type Options struct { Server *server.Server; Config *config.Config; Log *slog.Logger }
  type Console struct{ /* unexported */ }
  func New(o Options) (*Console, error)          // builds handler; no listening
  func (c *Console) Handler() http.Handler       // for httptest
  func (c *Console) Start() error                // listens on WebBind:WebPort (TLS per Task 4); returns after bind
  func (c *Console) Addr() string
  func (c *Console) Close() error
  ```

- Templates are parsed once from `embed.FS`. Every page uses `layout.html`, which contains the logo, the nav (Dashboard, Keys, Console, Pub/Sub, Logout) and the insecure banner slot.

- [ ] **Step 1: Write failing tests.**
  - `TestServer_SecurityHeaders`: GET `/static/app.css` and `/login` both carry the exact CSP from spec §4, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. Data pages carry `Cache-Control: no-store`.
  - `TestServer_StaticAssetsEmbedded`: `/static/htmx.min.js` returns 200 with `Content-Type` `text/javascript; charset=utf-8`.
  - `TestServer_PanicRecovered`: a test route that panics returns 500, and the next request still works.
  - `TestServer_UnknownRoute404`.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.**
  - Download htmx 2.0.4 `dist/htmx.min.js` and htmx-ext-sse 2.2.2 `dist/sse.js` from unpkg, and record their SHA-256 in the file headers.
  - Middleware order: recover → headers → (session/CSRF in Task 5) → mux.
  - `app.css` applies the theme tokens from Global Constraints.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): console skeleton, embedded assets, security headers`.

### Task 4: TLS modes and self-signed certificate

**Files:**
- Create: `internal/web/tls.go`
- Test: `internal/web/tls_test.go`

**Interfaces:**
- Produces:
  - `func resolveTLS(cfg *config.Config, dir string, log *slog.Logger) (*tls.Config, error)`. It returns nil for plain HTTP, and an error when the combination is refused (spec §5 table, row `off` + non-loopback with neither a trusted proxy nor insecure).
  - `func loadOrCreateSelfSigned(dir string, hosts []string, now time.Time) (tls.Certificate, error)`.

- [ ] **Step 1: Write failing tests.**
  - `TestTLS_ModeTable`: a table with one case per cell of spec §5: `auto`/`on`/`off` × loopback (`127.0.0.1`, `::1`) or non-loopback (`0.0.0.0`). It also covers `off` + non-loopback + trusted proxy → nil (allowed), and `off` + non-loopback + insecure → nil, with the warning logged.
  - `TestTLS_SelfSignedPersistedAndReused`:
    - Files land in `<dir>/web-cert.pem` and `<dir>/web-key.pem` with mode 0600.
    - The SANs include `localhost` and the given hosts.
    - The key is ECDSA P-256, valid for 365 days.
    - A second call returns the same certificate bytes.
    - With `now` 340 days later, the certificate is regenerated, because it is within 30 days of expiry.
  - `TestTLS_ProvidedCertUsed`: a certificate and key written by the test are loaded when `web-tls-cert` and `web-tls-key` are set.
  - `TestTLS_MinVersion12`.
  - `TestTLS_InsecureWarnsPeriodically`: with an injected ticker, insecure mode logs the warning at startup and again every 10 minutes, and the layout renders `class="insecure-banner"`.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.** When generating a certificate, log its SHA-256 fingerprint at info level (`web tls self-signed`, `fingerprint`).
- [ ] **Step 4: Run** `go test -race ./internal/web -run TLS` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): TLS auto/on/off with persisted self-signed cert`.

### Task 5: Authentication, sessions, CSRF, lockout

**Files:**
- Create: `internal/web/auth.go`, `internal/web/templates/login.html`
- Modify: `internal/web/server.go` (routes `/login` GET/POST and `/logout` POST; auth middleware), `internal/server/exec.go` (an authenticated-user option, if needed)
- Test: `internal/web/auth_test.go`

**Interfaces:**
- Consumes: `server.(*Server).NewSession() *server.Session`.
- Produces:
  - `type session struct{ id, csrf, user string; created, lastSeen time.Time; mu sync.Mutex; db *server.Session }`, with `mu` serialising every `db.Exec`.
  - `func (c *Console) sessionFrom(r *http.Request) *session` (nil when absent or expired).
  - `func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr`.
- `New` returns `ErrNoPassword` (`"web console requires requirepass (or web-insecure yes)"`) when `requirepass` is empty and `web-insecure` is off.

- [ ] **Step 1: Write failing tests.**
  - `TestAuth_LoginSuccessSetsCookie`: POST the correct password. The response is 303 to `/` and sets `dc_session` with HttpOnly and SameSite=Strict. It has no `Secure` flag over HTTP and does have it when the request arrived as TLS or `X-Forwarded-Proto: https` from a trusted proxy.
  - `TestAuth_WrongPasswordRejected`: 401 and the login page with a generic "invalid credentials" message.
  - `TestAuth_LockoutAfterFiveFailures`: the 6th attempt within 5 minutes from the same IP gets 429 even with the correct password. The lockout clears 5 minutes later; inject the clock through `Console.now`.
  - `TestAuth_LockoutUsesForwardedForOnlyFromTrustedProxy`.
  - `TestAuth_CSRFRequiredOnPost`: POST without the token → 403. With `X-CSRF-Token` → 200.
  - `TestAuth_IdleAndAbsoluteExpiry`: idle longer than `WebSessionTimeout` → redirect to /login. Active but older than 12h → redirect to /login.
  - `TestAuth_ExpiredSessionHTMXRedirect`: with an `HX-Request: true` header and an expired session, the response is 401 with an `HX-Redirect: /login` header.
  - `TestAuth_PasswordChangeAppliesToNewLogins`.
  - `TestAuth_NoPasswordRefusesStart`: `New` returns `ErrNoPassword`. With insecure on, `New` succeeds and login is skipped.
  - `TestAuth_LogoutDeletesSession`.
  - `TestAuth_AuditLog`: successful and failed logins each log one line with `user` and `ip` and never the password.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.**
  - Compare passwords with `crypto/subtle.ConstantTimeCompare` against `cfg.RequirePass()`.
  - Session IDs and CSRF tokens are 32 bytes of `crypto/rand`, encoded with base64url.
  - Sessions live in a mutex-guarded map, with a janitor goroutine every minute.
  - The CSRF token is exposed to htmx through `<meta name="csrf">` plus `hx-headers` on `<body>`.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): login, sessions, CSRF and lockout`.

### Task 6: Command console

**Files:**
- Create: `internal/web/console.go`, `internal/web/samples.go`, `internal/web/static/console.js`, `internal/web/render.go` (RESP → view model), `internal/web/templates/console.html`, `internal/web/templates/reply.html`
- Test: `internal/web/console_test.go`

**Interfaces:**
- Produces:
  - `func parseCommandLine(line string) ([]string, error)`, which follows redis-cli quoting: `"a b"`, `'x'`, and `\n`/`\x41` escapes inside double quotes.
  - `type replyView struct{ Kind string /* "status","error","int","bulk","nil","array","map","double","bool","verbatim" */; Text string; Binary bool; Items []replyView }`.
  - `func viewOf(v resp.Value) replyView`.
  - Route: POST `/console/exec` (form field `cmd`) returns the `reply.html` fragment.
  - The deny list is a `map[string]string` (command → reason) with: SUBSCRIBE, PSUBSCRIBE, SSUBSCRIBE, MONITOR, QUIT, RESET, CLIENT REPLY, plus BLPOP, BRPOP, BLMOVE, BRPOPLPUSH, BZPOPMIN, BZPOPMAX, BLMPOP, BZMPOP and XREAD/XREADGROUP with BLOCK, when the timeout is 0.

- Interactivity:
  - **Sample-command panel** (`internal/web/samples.go`): a static, curated list grouped by data type (Strings, Hashes, Lists, Sets, Sorted sets, Streams, Geo, HyperLogLog, Pub/Sub, Server). Each group has 3–6 runnable examples with a one-line description, using keys prefixed `sample:`. Clicking an example fills the input; Shift+click runs it. A "Load sample data" button runs a fixed seed script that creates one `sample:*` key of each type and refuses if any `sample:*` key already exists.
  - **Autocomplete and hints**: GET `/console/complete?q=` returns up to 10 command names matching the prefix, plus the syntax summary from `COMMAND DOCS` (cached once per process). It renders as a datalist/hint line under the input.
  - Keyboard: Up/Down walks history, Tab accepts a completion, Ctrl+L clears the output. This lives in `static/console.js`, which is plain JS under the CSP (no inline scripts).
- [ ] **Step 1: Write failing tests.**
  - `TestConsole_SamplesAreValidCommands`: every sample command parses, names a command that exists in the command table, and runs on a fresh server without an error reply after the seed script.
  - `TestConsole_SeedRefusesWhenSamplesExist`.
  - `TestConsole_CompletePrefix`: `q=hg` returns HGET, HGETALL and so on, with a non-empty syntax hint; `q=` returns nothing.
  - `TestConsole_ParseCommandLine`: table cases `set k "a b"` → `[set k a b]`, `'it''s'` errors, `"\x41"` → `A`, and an unbalanced quote errors.
  - `TestConsole_ExecRendersReplies`: `PING` → status PONG, `INCR n` → int, `GET missing` → nil, `HGETALL h` → map, and an error reply rendered with the `error` kind.
  - `TestConsole_DeniedCommands`: `SUBSCRIBE x`, `MONITOR` and `BLPOP k 0` get a refusal fragment naming the reason, and nothing is executed (MONITOR not registered). `BLPOP k 1` executes.
  - `TestConsole_HistoryPerSession` keeps the last 100 commands in the session, newest first.
  - `TestConsole_ConcurrentRequestsSameSession`: 8 goroutines POST INCR through one session under `-race`, and the final value is 8×N.
  - `TestConsole_AuditLogsCommandNameNotArgs`: `SET secret hunter2` logs `cmd=SET keys=1` and the log does not contain `hunter2`.
  - `TestConsole_BinaryRenderedAsHex`: `SET b "\xff\x00"` and then `GET b` render with `Binary` true and hex text `ff 00`.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement** using the session's `db.Exec` while holding `session.mu`.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): command console`.

### Task 7: Dashboard and live stats stream

**Files:**
- Create: `internal/web/dashboard.go`, `internal/web/templates/dashboard.html`, `internal/web/templates/tiles.html`
- Test: `internal/web/dashboard_test.go`

**Interfaces:**
- Consumes: `(*server.Server).Stats() StatsSnapshot` (Task 2).
- Produces:
  - `type sampler struct` with a ring of 300 `StatsSnapshot` entries, sampled every 1 s by one goroutine per Console that stops on `Close`.
  - `func (s *sampler) last(n int) []server.StatsSnapshot`.
  - Route GET `/events/stats` is an SSE stream (`Content-Type: text/event-stream`) that emits `event: tiles` with the rendered `tiles.html` fragment once per sample.
  - Tiles: ops/s, p50/p99 latency (from INFO latencystats via a `Session.Exec("INFO","latencystats")` parse, or blank if unavailable), memory vs maxmemory, clients, hit rate (`Hits/(Hits+Misses)`, shown as "—" when 0/0), evictions, expired, role.
  - Sparklines are inline SVG `<polyline>` elements over the last 300 samples.
  - The heatmap is an SVG grid with one cell per shard. Its colour intensity is ops in the last sample, and an outline marks lock-wait above zero.

  - **Memory limit control** on the memory tile: a form posting to POST `/settings/memory` with fields `maxmemory` (a Redis size string such as `4gb`, `512mb` or `0`) and `policy` (one of the eight Redis policies, chosen from a select). It runs `CONFIG SET maxmemory <v>` and `CONFIG SET maxmemory-policy <p>` through the browser session, requires CSRF, and writes an audit log line (`setting=maxmemory value=4gb`). It returns the refreshed tile, or the server's error reply inline (for example an invalid size). This is the only editing control in phase A.
- [ ] **Step 1: Write failing tests.**
  - `TestDashboard_SetMaxMemory`: POST `maxmemory=4gb&policy=allkeys-lru` with CSRF, then `CONFIG GET maxmemory` returns `4294967296` and the policy is `allkeys-lru`. `maxmemory=4zb` renders the inline error and leaves the setting unchanged. Without CSRF the request gets 403. The audit line is present.
  - `TestDashboard_SamplerRing`: with an injected tick, 305 samples leave 300 kept in order.
  - `TestDashboard_SSEEmitsTiles`: connecting to `/events/stats` with a valid session yields an `event: tiles` frame containing `ops/s` within 2 s. Disconnecting the client ends the handler goroutine (check the goroutine count is stable).
  - `TestDashboard_HitRateZeroDivision` renders "—".
  - `TestDashboard_HeatmapHasCellPerShard`: with 16 shards there are 16 `<rect class="shard"` elements.
  - `TestDashboard_RequiresSession`: no cookie → redirect to /login.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.** The SSE handler flushes after each event, sends a `: keepalive` comment every 15 s and returns when `r.Context().Done()` fires.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): dashboard with live tiles, sparklines and shard heatmap`.

### Task 8: Key browser and type viewers

**Files:**
- Create: `internal/web/keys.go`, `internal/web/templates/keys.html`, `internal/web/templates/key.html`, `internal/web/templates/viewers/{string,hash,list,set,zset,stream,geo,hll}.html`
- Test: `internal/web/keys_test.go`

**Interfaces:**
- Routes:
  - GET `/keys?pattern=&type=&cursor=` returns the page, or a rows fragment when `HX-Request` is set. It uses `SCAN cursor MATCH pattern COUNT 200 [TYPE t]`, and each row has TYPE, TTL and MEMORY USAGE pipelined through the session.
  - GET `/key?k=<base64url(key)>&page=N`. Key names are always base64url-encoded in URLs.
- Paging: each page holds 100 elements, and strings are truncated after 64 KiB with a "showing 64 KiB of N" note.
- Geo detection: a zset is treated as geo when GEOPOS of its first 10 members all return coordinates and every score is below 2^52. The page then offers a "Map" tab beside the table.
- String view: valid JSON → pretty-printed; valid UTF-8 → text; otherwise hex.

- [ ] **Step 1: Write failing tests.**
  - `TestKeys_ScanPagesThroughThousandKeys`: 1,000 keys are seeded. Following `cursor` until it reaches 0 returns every key exactly once, and no request sends `KEYS`; assert through the server's commandstats.
  - `TestKeys_TypeFilter`.
  - `TestKeys_EachTypeRenders`: seeded string, JSON string, binary string, hash, list, set, zset, stream, geo and HLL keys each return 200 with the expected viewer marker (`data-viewer="hash"` and so on).
  - `TestKeys_LargeValuesArePaged`: a 5 MB string renders at most 64 KiB plus the note. A 10,000-field hash page 1 shows 100 rows and a next-page link.
  - `TestKeys_OddKeyNamesRoundTrip`: keys `a/b`, `x?y#z`, `sp ace`, `ключ` and `"\xff\xfe"` each open the right key through the link in the list.
  - `TestKeys_HostileValuesEscaped`: a key named `<script>alert(1)</script>` with value `"><img src=x onerror=alert(1)>` produces HTML that contains no unescaped `<script` or `onerror=` in either the list or the key page.
  - `TestKeys_MissingKey404` covers a key deleted between the list and the open.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement** the routes and viewers with the paging and detection rules above.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): key browser with per-type viewers`.

### Task 8a: Pub/Sub page

**Files:**
- Create: `internal/web/pubsub.go`, `internal/web/templates/pubsub.html`
- Test: `internal/web/pubsub_test.go`

**Interfaces:**
- Consumes: `server.(*Server).NewSession()`, `Session.Exec`, `Session.ReadPush` (existing, exec.go), session/CSRF from Task 5, `replyView` from Task 6.
- Routes:
  - GET `/pubsub` shows the page: active channels from `PUBSUB CHANNELS`, with counts from `PUBSUB NUMSUB` and `PUBSUB NUMPAT`, plus a publish form and a subscribe form.
  - POST `/pubsub/publish` (fields `channel`, `message`) runs `PUBLISH` on the browser session and returns the receiver count.
  - GET `/events/pubsub?ch=a,b&pat=news.*` is an SSE stream. It opens a dedicated `server.Session` for this stream (never the browser session's), SUBSCRIBEs or PSUBSCRIBEs, and forwards each push as `event: message` with a rendered row (time, channel, pattern, payload, hex if binary).
  - At most 20 channels/patterns per stream and 4 open streams per browser session; above that the server replies 429.
  - The stream session is closed when the browser disconnects, so the subscription disappears from `PUBSUB NUMSUB`.
  - Payloads are truncated to 4 KiB in the UI. The view shows the last 500 messages, with pause/resume and clear done client-side.

- [ ] **Step 1: Write failing tests.**
  - `TestPubSub_PublishReturnsReceivers`.
  - `TestPubSub_StreamReceivesMessages`: open the SSE stream for `ch=news`, PUBLISH 3 messages over RESP, and the stream yields 3 `event: message` frames in order within 2 s.
  - `TestPubSub_PatternSubscribe`: `pat=news.*` receives `news.tech` and not `sport`.
  - `TestPubSub_DisconnectUnsubscribes`: after the client cancels, `PUBSUB NUMSUB news` returns 0 within 1 s.
  - `TestPubSub_Limits`: 21 channels → 400; a 5th stream → 429.
  - `TestPubSub_HostilePayloadEscaped`.
  - `TestPubSub_RequiresSessionAndCSRF`: publishing without CSRF → 403.
- [ ] **Step 2: Run** the tests — expect FAIL.
- [ ] **Step 3: Implement.** Each stream is one goroutine that reads pushes with ReadPush and exits on `r.Context().Done()`. It sends a `: keepalive` comment every 15 s.
- [ ] **Step 4: Run** `go test -race ./internal/web` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): pub/sub page with live subscribe and publish`.

### Task 9: Wire-up, end-to-end test, docs

**Files:**
- Modify: `cmd/distcache-server/main.go` (start the console when `cfg.WebPort > 0`; log the error and continue on failure; close on shutdown), `README.md` (a "Web console" section), `CLAUDE.md` (an `internal/web` line), `docs/architecture.md` (the console in the big-picture diagram plus the code map)
- Create: `docs/guides/02-web-console.md`, `test/e2e/web_test.go`

- [ ] **Step 1: Write failing e2e test** `TestWebConsole_LoginDashboardKey`:
  - Start the binary with `--requirepass pw --web-port <free>`.
  - Using `net/http` with a cookie jar: GET `/login` → 200; POST the password → redirect; GET `/` → 200 containing `ops/s`.
  - SET a key over RESP, then GET `/key?k=…` → 200 containing the value.
  - Second test `TestWebConsole_BindFailureDoesNotStopServer`: occupy the web port first. The server still answers PING on RESP, and its log contains `web console disabled`.
- [ ] **Step 2: Run** `cd test/e2e && go test -run WebConsole` — expect FAIL.
- [ ] **Step 3: Implement the wire-up and write the docs.**
  - The guide covers enabling the console, each TLS mode with a sample command, reverse-proxy setup (one nginx snippet), the security model and a page tour.
- [ ] **Step 4: Verify.** Run `go build ./... && go vet ./... && go test -race -count=1 ./... && (cd test/e2e && go test -race -count=1 ./...)` — expect all PASS. Then open each page in a browser via Playwright against a local server (login, dashboard updating, keys, one viewer per type, console with samples, pub/sub live) and save screenshots to the scratchpad.
- [ ] **Step 5: Commit** `feat(web): start console from distcache-server; e2e test and guide`.
