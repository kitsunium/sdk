# pkg/v1/process

Public facade for the SDK's keystone process-spawn primitive (ADR 0016). Start a
child under explicit credentials, in its own process group/session, and get a
handle that waits, signals (leader or group), and stops the group gracefully —
and, since ADR 0100, read the running process itself: its runtime state and
the build it came from.

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
- **Stdio re-exports** (`stdio.go`): `StdioMode` and `StdioInherit` /
  `StdioNull` / `StdioCapture`, so a consumer sets `Spec.Stdio` — and captures
  a child's output into any `io.Writer` — with public names only.
  `TestCaptureAndBareNamesThroughTheFacade` is that consumer.

## Surface

| Symbol | Notes |
|---|---|
| `Spec`, `Process`, `ExitResult`, `Signal`, `Resource`, `Limit` | aliases of the `internal/core/proc` port |
| `StdioMode`, `StdioInherit` / `StdioNull` / `StdioCapture` | `Spec.Stdio`; capture delivers every byte before `Wait` returns |
| `SIGTERM` / `SIGKILL` / `SIGINT` / `SIGHUP` / `SIGQUIT` | typed `Signal` constants |
| `ResourceNoFile` / `ResourceCore` / `ResourceCPU` / `ResourceAS` | the common `Spec.Rlimits` keys |
| `Start`, `MustStart` | spawn; a bare `Spec.Path` is searched in the child's PATH (Spec.Env's, else the parent's), `exec.ErrDot`/`ErrNotFound` wrapped in `SpawnFailed` |
- **README is generated.** `README.md` is produced by `gomarkdoc` from the
  package doc comment in `process.go` (Rule 10). Edit the doc comment, then
  `make docs-readme` (or run the `//go:generate` line). Maintainer rationale
  (this file) stays in `CLAUDE.md`.

## The process itself (ADR 0100)

`Self()`, `Build()` and `ParseBuild()` delegate to `internal/service/proc/self`,
and `Stats` / `Distribution` / `BuildInfo` / `Module` alias its values — the
facade's rule is unchanged: aliases and one-line delegations, no new named
types. They read the process the package runs in, on every platform, and none
of them can fail: `Stats.CPUEstimated` says when `CPUTime` is the runtime's
estimate rather than the kernel's count, and `Build` reports `false` for a
binary without build information.

A `Module` keeps apart what a recorded version conflates: a release
(`Version`), a commit (`Revision`, `Time`) and a local directory (`Local`,
`Dir`). What that directory holds NOW is `pkg/v1/git`'s `Head`.

## Do / Do-not

- **Do** pass `os.Environ()` explicitly when the child should inherit the
  environment — nil `Spec.Env` is an empty environment by design.
- **Do** set `Setpgid` when you want `Stop`/`SignalGroup` to reach the whole
  tree (the common case for supervising a shell or a process that forks).
- **Do** rely on `Spec.Umask` / `Spec.Rlimits` being applied — the stdlib spawn
  cannot run `setrlimit`/`umask` in the child, so `Start` routes through a
  re-exec trampoline that applies them before the target execs. A kernel-refused
  limit fails the spawn (`RlimitFailed`); an unmappable resource is rejected up
  front (`UnknownResource`). `Nice` and `OOMScoreAdj` apply post-start.
- **Do not** add new exported types here — the public surface is aliases only.

## Errors

Every error is a central `internal/core/proc` sentinel; match with
`errs.HasCode(err, coreproc.CodeX)` (or, downstream, the read-only
`pkg/v1/errs` introspection helpers). `Start` surfaces `InvalidSpec`,
`UnknownUser`, `UnknownGroup`, `UnknownResource`, `RlimitFailed`, `SpawnFailed`,
or `UnsupportedPlatform` (a Unix-only Spec field on Windows, or a platform
with no spawn backend); the handle surfaces `WaitFailed`,
`SignalFailed`, `StopFailed`.

## Platform

Unix gets every field. Windows spawns too (`internal/service/proc/exec/exec_windows.go`):
stdio, `Setpgid` as a new console process group, `Spec.Rlimits` through a Job
Object, and the PATH search with PATHEXT; `User`/`Group`/`Groups`, `Umask`,
`Nice`, `OOMScoreAdj`, `ExtraFiles` and `CgroupPath` are refused with
`UnsupportedPlatform` rather than dropped, `SignalGroup` reaches the leader
only, and `Stop` escalates with TerminateProcess. Every other GOOS gets
`UnsupportedPlatform` from `Start` (`process_other_test.go`, gated
`!unix && !windows`); the package compiles everywhere.

The facade suite follows the same split: `process_other_test.go`
(`!unix && !windows`) asserts the refusal where there is no backend,
`process_windows_test.go` asserts the Windows backend through the facade. The
facade test used to carry `!unix` and assert the refusal on Windows too, a year
after Windows gained its backend; the first Windows run of the suite found it
(ADR 0095).
