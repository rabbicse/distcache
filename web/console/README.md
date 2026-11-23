# Web console

The console is built into the server binary and lives in
[internal/web](../../internal/web): Go `html/template` pages with htmx, embedded
with `go:embed`. There is no separate front-end project and nothing to build
here.

Design: [docs/superpowers/specs/2026-11-10-web-console-design.md](../../docs/superpowers/specs/2026-11-10-web-console-design.md).
Status: login, dashboard and command console are written and tested;
`distcache-server` does not start the console yet.
