# pkg/v1/process

Public facade for the SDK's keystone process-spawn primitive (ADR 0016). Start a
child under explicit credentials, in its own process group/session, and get a
handle that waits, signals (leader or group), and stops the group gracefully.

## Why this shape

- **Thin facade, alias types.** `Spec`, `Process`, `ExitResult`, `Signal`,
  `Resource`, `Limit` are **type aliases** of the `internal/core/proc` port — a
  value built here is the exact type the service layer consumes. No conversion,
  no parallel hierarchy. `Start` delegates straight to
  `internal/service/proc/exec.Start`; the facade adds zero behaviour.
- **Ergonomic re-exports** (`signals.go`): the common signal constants
  (`SIGTERM`/`SIGKILL`/`SIGINT`/`SIGHUP`/`SIGQUIT`) and the most-used resource
  sentinels, so callers drive `Stop`/`SignalGroup` and build `Spec.Rlimits`
  without importing `internal/core/proc` (which they cannot — it is internal).
- **README is generated.** `README.md` is produced by `gomarkdoc` from the
  package doc comment in `process.go` (Rule 10). Edit the doc comment, then
  `make docs-readme` (or run the `//go:generate` line). Maintainer rationale
  (this file) stays in `CLAUDE.md`.

## Do / Do-not

- **Do** pass `os.Environ()` explicitly when the child should inherit the
  environment — nil `Spec.Env` is an empty environment by design.
- **Do** set `Setpgid` when you want `Stop`/`SignalGroup` to reach the whole
  tree (the common case for supervising a shell or a process that forks).
- **Do not** expect `Spec.Umask` / `Spec.Rlimits` to apply — the stdlib spawn
  cannot run `setrlimit`/`umask` in the child, so these return `RlimitFailed`
  rather than lying. `Nice` and `OOMScoreAdj` *do* apply (post-start).
- **Do not** add new exported types here — the public surface is aliases only.

## Errors

Every error is a central `internal/core/proc` sentinel; match with
`errs.HasCode(err, coreproc.CodeX)` (or, downstream, the read-only
`pkg/v1/errs` introspection helpers). `Start` surfaces `InvalidSpec`,
`UnknownUser`, `UnknownGroup`, `UnknownResource`, `RlimitFailed`, `SpawnFailed`,
or `UnsupportedPlatform` (non-Unix); the handle surfaces `WaitFailed`,
`SignalFailed`, `StopFailed`.

## Platform

Unix only for actual supervision. On non-Unix targets `Start` returns
`UnsupportedPlatform`; the package compiles on every GOOS.
