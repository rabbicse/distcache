# Go conventions for DistCache (binding for every task)

Sources: Effective Go, Go Code Review Comments, Go Test Comments. Go 1.27.

Naming: MixedCaps; no stutter (package storage → `storage.Keyspace`, not `storage.StorageKeyspace`); initialisms uppercase (`ID`, `URL`, `AOF`, `TTL`, `RESP`, `LRU`); receivers 1–2 letters, consistent per type; no `Get` prefix on getters; errors named `errFoo`/`ErrFoo`, error strings lowercase without trailing punctuation (EXCEPT Redis wire error strings, which must match Redis exactly); interface names end in -er when single-method.
Comments: every exported identifier has a doc comment starting with its name, full sentence; file-level comment on responsibility for new files; comments say why, not what. Package doc in doc.go or the main file.
Errors: wrap with `fmt.Errorf("...: %w", err)`; check with `errors.Is/As`; never ignore an error silently — `_ =` only with a comment when intentionally ignored.
Concurrency: document which mutex guards which fields (comment next to the field); never copy a struct containing sync types; prefer `atomic.Int64` etc. typed atomics; every goroutine has a clear owner and exit path; tests pass under `-race`.
Code: keep functions small; early returns over nested ifs; no naked returns in long functions; use `slices`/`maps`/`min`/`max` builtins and range-over-int where they simplify; no global mutable state added; no `panic` in library code except programmer errors; `context.Context` first param when added.
Tests: table-driven with `t.Run(tc.name, ...)`; names `TestType_Method_Scenario` or `TestFeature_Scenario`; `t.Helper()` in helpers; `t.Cleanup` over defer for fixtures; `t.TempDir()` for files; failure messages `got X, want Y`; no sleeps where polling with deadline works; no external network.
Formatting: gofmt/goimports clean; `go vet` and `golangci-lint` clean.
Portability: Linux and macOS are the targets; keep code Windows-compilable — use `filepath`, never hardcode `/`; no unix-only syscalls outside build-tagged files.
