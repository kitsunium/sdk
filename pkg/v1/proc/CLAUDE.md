<!-- updated: 2026-10-05T00:00:00Z -->
# pkg/v1/proc

## Purpose

The **capability-preflight** facade for the process-supervision domain (ADR 0016).
It does not spawn, signal, or confine anything — the per-capability facades, its
children (`process`, `signal`, `reaper`, `rlimit`, `cgroup`, `systemd/notify`,
`systemd/listen`), do that. This package lets a CONSUMER ask, up front, *which
proc capabilities have a native backend on the current platform* and choose to
fail fast when a required one is missing.

It is also the root of the proc family (ADR 0155): every facade of the family is
its child. A child is a package of its own — importing `pkg/v1/proc/process`
links neither this package nor any sibling, because Go links what a package
imports and never its parent directory (checked below), and this package imports
no child either.

## The family

| Child | What it publishes | Engine under `internal/service/proc` |
|---|---|---|
| `process/` | `Start` / `MustStart` → a `Process` to wait on, signal and stop; and the running program itself — `Self`, `Build`, `ParseBuild` (ADR 0016, ADR 0100) | `exec`, `self` (and `childwait`, which `exec` and `reaper` share) |
| `signal/` | `Notify`, `Relay` to a `Target`, `Parse` | `signal` |
| `reaper/` | PID 1 / subreaper zombie collection (ADR 0016, ADR 0093) | `reaper` |
| `rlimit/` | per-process resource limits over `Resource` | `rlimit` |
| `cgroup/` | control groups — `Create` / `MustCreate` → a `Group`, `Available` | `cgroup` |
| `memlimit/` | the runtime soft memory limit from the cap already bounding this process (ADR 0075) | `memlimit` |
| `systemd/notify/` | sd_notify(3): the notifier, and `Listen` for a supervisor | `systemd/notify` |
| `systemd/listen/` | socket activation, sd_listen_fds(3), and `Prepare` for an activator | `systemd/listen` |
| `ipc/` | a private socket between processes of one machine, the peer the kernel names (ADR 0148); `Listener` and `Dialer` are ports a test can double (ADR 0160) | `ipc` |

`systemd/` holds no Go code: it groups the two systemd protocols, and each was
renamed for the path it sits at — package `notify`, formerly `sdnotify`, and
package `listen`, formerly `sdlisten` (`systemd/CLAUDE.md`). Every child but
`ipc` builds on `internal/core/proc` — its ports, values and `0.2.6.*`
sentinels; `ipc` builds on its own contract, `internal/core/proc/ipc` — the
`Listener` and `Dialer` ports, `Peer`, `Conn` and the `0.3.91.*` codes it
re-exports (ADR 0160). Each child has its own `CLAUDE.md` and generated
`README.md`.

## Why this exists

The SDK honours "typed errors only, never panic": every platform gap returns
`UnsupportedPlatform`, the library never panics on its own (verified: zero
`panic(` in `internal/{core,service}/proc`). But a consumer — especially a PID1
supervisor — needs to be *conscious* of capability gaps WITHOUT a `recover()` in
its hot path (an unrecovered panic in pid 1 orphans every supervised service; a
panic across a CGO/FFI boundary is UB). So this package gives the consumer the
**opt-in** fail-fast, idiomatic-Go `MustX` style (`regexp.MustCompile`): the SDK
*offers* the panic, it never imposes it.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Capability` | type | one per platform-sensitive primitive (`CapProcessSpawn`, `CapRlimit`, `CapUmaskNiceOOM`, `CapCgroup`, `CapReaper`, `CapSignalRelay`, `CapSdNotify`, `CapSocketActivation`) |
| `Supported(cap)` | func | platform-level matrix (`runtime.GOOS` only) — **pure**, no syscall |
| `MissingCapabilities(caps...)` | func | the unsupported subset; nil = all present |
| `MustSupport(caps...)` | func | **panics** (typed `UnsupportedPlatform` error) when any cap is missing; no-op otherwise — the consumer's opt-in |
| `UnsupportedPlatform` | var | the central sentinel re-exported for recover handlers |

The `MustX` constructors of the children are the same pattern:
`cgroup.MustCreate`, `process.MustStart` — thin wrappers that panic with the
same typed error their `Create`/`Start` form returns.

## Contract (non-negotiable)

- **No panic at `init`/import** — importing `proc` for `Supported`/preflight must
  never crash a consumer; the SDK never panics on its own.
- **The panic payload is the typed `errs` value** (code `0.2.6.1`
  UNSUPPORTED_PLATFORM), never a bare string — a top-level `recover()` classifies
  it via `pkg/v1/errs.CodeOf` / `HasCode`.
- **`Supported` is platform-level, not a runtime probe.** It reflects whether a
  native backend exists on the GOOS, NOT runtime availability (e.g. cgroup present
  but not delegated). Use the per-facade probe for that (`cgroup.Available()`).
- The capability × platform matrix lives in the package doc comment (`doc.go`,
  written by kit from the design — ADR 0167) and is mirrored in the generated
  `README.md`.
- **A GOOS joins a row only on the evidence of its own kernel.** `illumos` and
  `solaris` are listed apart — `runtime.GOOS` names them apart although the
  `solaris` build tag selects both — and joined the matrix only once
  `e2e-cross.yml`'s `solarish` legs ran the proc suites there (ADR 0144). That
  lane runs this package's suite too, so `TestSupported` checks each row on the
  kernel it describes; the test's `isUnixGOOS` mirrors `unixTargets`.

## Intended use (consumer side)

```go
func main() {
    proc.MustSupport(proc.CapProcessSpawn, proc.CapReaper) // crash-at-launch, clear
    os.Exit(run())
}
// elsewhere, branch instead of crash for an optional capability:
if proc.Supported(proc.CapCgroup) { /* confine */ }
```

## README is generated

`README.md` is written by `tools/genindex` from the committed `docs/api`
(`make docs-readme`, ADR 0167). Do **not** hand-edit it. The package comment is
in `doc.go`, which kit writes from the design (`design/proc.yaml`): edit the
design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Import a child from this package's production code, or this package from a
  child's. The family shares a directory, not a link: a consumer of `process`
  must not pay for the preflight, nor a consumer of `Supported` for nine
  facades.
- Put Go code in `systemd/`: it would publish a package nobody designed at the
  path both protocols appear to belong to.

## Verification

```sh
bazel test --config=race //pkg/v1/proc:proc_test
cd pkg && GOWORK=off go test -race ./v1/proc
# The whole family
bazel test --config=race //pkg/v1/proc/...
cd pkg && GOWORK=off go test -race ./v1/proc/...
# A child never links its parent (ADR 0155) — must print nothing:
cd pkg && for c in process signal reaper rlimit cgroup memlimit ipc systemd/notify systemd/listen; do
  GOWORK=off go list -deps ./v1/proc/$c | grep -x 'github.com/kitsunium/sdk/pkg/v1/proc'
done
```

## Subtree

`process/`, `signal/`, `reaper/`, `rlimit/`, `cgroup/`, `memlimit/`, `ipc/`,
`systemd/notify/`, `systemd/listen/` — each documents its own facade in its
`CLAUDE.md`; `systemd/CLAUDE.md` names the two protocols and why they are
grouped.

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declaration of `Capability`; `Supported` and `MissingCapabilities`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name.
