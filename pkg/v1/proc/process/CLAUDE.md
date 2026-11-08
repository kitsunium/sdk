<!-- updated: 2026-11-08T00:00:00Z -->
# pkg/v1/proc/process

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
- **Stdio re-exports** (`gen_facade.go` since ADR 0166; `stdio.go`, which held them, is gone, and the package comment is `doc.go`'s since ADR 0167): `StdioMode` and `StdioInherit` /
  `StdioNull` / `StdioCapture`, so a consumer sets `Spec.Stdio` — and captures
  a child's output into any `io.Writer` — with public names only.
  `TestCaptureAndBareNamesThroughTheFacade` is that consumer. It writes the
  program it runs under `syscall.ForkLock` (`writeExecutable`): a sibling
  test forking while the file was open for writing kept a copy of the
  descriptor until its own exec, and `execve` refused the probe with
  `ETXTBSY` — 15 runs in 300, issue #269. The SDK reported that refusal
  correctly; the race was the test's.

## Surface

| Symbol | Notes |
|---|---|
| `Spec`, `Process`, `ExitResult`, `Signal`, `Resource`, `Limit` | aliases of the `internal/core/proc` port |
| `StdioMode`, `StdioInherit` / `StdioNull` / `StdioCapture` | `Spec.Stdio`; capture delivers every byte before `Wait` returns |
| `SIGTERM` / `SIGKILL` / `SIGINT` / `SIGHUP` / `SIGQUIT` | typed `Signal` constants |
| `ResourceNoFile` / `ResourceCore` / `ResourceCPU` / `ResourceAS` | the common `Spec.Rlimits` keys |
| `Start`, `MustStart` | spawn; a bare `Spec.Path` is searched in the child's PATH (Spec.Env's, else the parent's), `exec.ErrDot`/`ErrNotFound` wrapped in `SpawnFailed` |
| `StdioPTY`, `PTYSize`, `Terminal` | a child on a pseudo-terminal; the master is `Terminal.TTY` (Linux, darwin, FreeBSD — ADR 0217) |
| `ContextWaiter`, `Run`, `RunOptions`, `Result` | a wait bounded by a context; a one-shot run keeping the first `MaxOutputBytes` of each stream (#935) |
| `EnvAllow`, `EnvDeny` | environment allow/deny lists, parent order kept, a repeated key keeping its last value |
| `ReservedEnv`, `CodeReservedEnv` | the refusal of a `Spec.Env` key beginning `__KITSUNIUM_SDK_PROC_`, any case — the SDK's own spawn protocol, which the key would override (#726); re-exported so a consumer matches it with `errors.Is` |
| `ProcessHandle`, `Open` | signals through a pidfd/HANDLE, never a reused pid; `Open` holds a process not spawned here, its `Wait` refused (#928) |
| `Attach`, `AttachPIDFile`, `AttachScope` | adopt a process not spawned here, its end observed — `Wait` returns `StatusUnknown` —, from a pid or a PID file read safely; an untrustworthy file adopts only within the scope's cgroup and users (#729; Linux through a pidfd, darwin and the BSDs through kqueue knotes — a signal by pid only while a knote proves the process alive, a cgroup scope refused — never run on the authoring host) |
| `PIDFileRefused`, `AdoptionRefused`, `StatusUnknown` + their `Code*` | #729's refusals and end, re-exported for `errors.Is` |
| `Scheduling`, `SchedPolicy`, `IOClass` + the `Sched*` / `IOClass*` constants | `Spec.Scheduling` (#940) |
| `Sandbox`, `Namespace`, `IDMap`, `Mount`, `MountKind`, `Capabilities`, `Landlock`, `LandlockRule`, `LandlockAccess`, `Seccomp`, `SeccompAction` + the `Namespace*` / `Mount*` / `Landlock*` / `Seccomp*` constants | `Spec.Sandbox` (#728): the core port's values, re-exported |
| `LandlockABI` | forwards `exec.LandlockABI`: the kernel's Landlock ABI, `SandboxUnavailable` without it, `UnsupportedPlatform` off Linux amd64/arm64 |
| `SandboxFailed`, `SandboxUnavailable`, `CodeSandboxFailed`, `CodeSandboxUnavailable` | a sandbox step failed in the child (the program never ran) / a part the kernel lacks, refused before the fork (#728) |
| `Usage` | `ExitResult.Usage`: faults, block I/O, context switches, nil where the platform gives none (#1090) |
| `FDPolicy`, `FDsSealed`, `FDsInherited` | `Spec.FDs`: the child's descriptor table sealed by default (#1089) |
| `SetOOMScoreAdj` | the running process's own OOM-killer bias (Linux; `internal/service/proc/self`) |
| `Self`, `Build`, `ParseBuild`; `Stats`, `Distribution`, `BuildInfo`, `Module` | the running process and the build it came from — one-line delegations to, and aliases of, `internal/service/proc/self` (`Self`, `Stats`, `Distribution`) and `internal/service/proc/buildinfo` (`Build`, `ParseBuild`, `BuildInfo`, `Module`; ADR 0100, ADR 0172) |
| `ReadIdentity`, `Identity`, `StartSource` + `StartSourceInit` / `StartSourceKernel` | what the kernel says the process is: start time, binary digest and build id, container, pid namespace, boot (#938) |
| `Toolchain`, `Setting` | how the toolchain built the binary (`BuildInfo.Toolchain`: target, feature level, flags, cgo, trimpath) and every recorded setting verbatim (`BuildInfo.Settings`); `BuildInfo.Reproducible()` (#1095) |

- **README is generated.** `README.md` is written by `tools/genindex` from the
  committed `docs/api` (`make docs-readme`, ADR 0167). The package comment is in
  `doc.go`, which kit writes from the design (`design/proc.yaml`): edit the
  design, run `kit gen`, then `make api` and `make docs-readme`. Maintainer
  rationale (this file) stays in `CLAUDE.md`.

## The process itself (ADR 0100)

`Self()` delegates to `internal/service/proc/self` and `Stats` / `Distribution`
alias its values; `Build()` and `ParseBuild()` delegate to
`internal/service/proc/buildinfo` and `BuildInfo` / `Module` alias its values,
the same types `pkg/v1/proc/buildinfo` publishes alone for a program that
reads its build and must not import runtime/metrics or the spawn engine
(ADR 0172) — the
facade's rule is unchanged: aliases and one-line delegations, no new named
types. They read the process the package runs in, on every platform, and none
of them can fail: `Stats.CPUEstimated` says when `CPUTime` is the runtime's
estimate rather than the kernel's count, and `Build` reports `false` for a
binary without build information.

A `Module` keeps apart what a recorded version conflates: a release
(`Version`), a commit (`Revision`, `Time`) and a local directory (`Local`,
`Dir`). What that directory holds NOW is `framework/git`'s `Head`.

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
`ReservedEnv` (re-exported here, #726),
`UnknownUser`, `UnknownGroup`, `UnknownResource`, `RlimitFailed`, `SpawnFailed`,
`CgroupUnavailable` / `CgroupWriteFailed` (a Linux `Spec.CgroupPath` that is
not a cgroup v2 directory, or whose placement the kernel refused), or
`UnsupportedPlatform` (a Unix-only Spec field on Windows, or a platform
with no spawn backend); the handle surfaces `WaitFailed`,
`StdioCaptureFailed` (from `Wait`, when a capture writer failed and the exit
status still stands), `SignalFailed`, `StopFailed`.

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

## Generated

`gen_facade.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `gen_facade.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `gen_facade.go` edited by hand.
