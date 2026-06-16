# pkg/v1/proc

## Purpose

The **capability-preflight** facade for the process-supervision domain (ADR 0016).
It does not spawn, signal, or confine anything — the per-capability facades
(`process`, `signal`, `reaper`, `rlimit`, `cgroup`, `sdnotify`, `sdlisten`) do
that. This package lets a CONSUMER ask, up front, *which proc capabilities have a
native backend on the current platform* and choose to fail fast when a required
one is missing.

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

Sibling `MustX` constructors live in the facades: `cgroup.MustCreate`,
`process.MustStart` — thin wrappers that panic with the same typed error their
`Create`/`Start` form returns.

## Contract (non-negotiable)

- **No panic at `init`/import** — importing `proc` for `Supported`/preflight must
  never crash a consumer; the SDK never panics on its own.
- **The panic payload is the typed `errs` value** (code `0.2.6.1`
  UNSUPPORTED_PLATFORM), never a bare string — a top-level `recover()` classifies
  it via `pkg/v1/errs.CodeOf` / `HasCode`.
- **`Supported` is platform-level, not a runtime probe.** It reflects whether a
  native backend exists on the GOOS, NOT runtime availability (e.g. cgroup present
  but not delegated). Use the per-facade probe for that (`cgroup.Available()`).
- The capability × platform matrix lives in the package doc comment (`capability.go`)
  and is mirrored in the generated `README.md`.

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

`README.md` is produced by `gomarkdoc` from the `capability.go` package doc
(ADR 0008). Edit the doc comment, then `make docs-readme`. Do **not** hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/proc:proc_test
cd pkg && GOWORK=off go test -race ./v1/proc/...
```
