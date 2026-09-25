# internal/service/proc/self/

## Purpose

What the running process can say about ITSELF (ADR 0100): the build it came
from (`build.go`) and its runtime state (`stats.go`, `cputime_*.go`). Every
other `proc` package acts on another process — spawns, signals, reaps, limits a
child; this one only reads, and only the process it runs in. Public facade:
`pkg/v1/process` (`Self`, `Build`, `ParseBuild`, and the `Stats` /
`Distribution` / `BuildInfo` / `Module` aliases).

Stdlib plus `golang.org/x/mod/module`, which the service module already
required (for `semver`) — no new module.

## Contents

| File | Role |
|---|---|
| `build.go` | package doc, `BuildValue` + `Module(path)`, `ModuleValue`, `ReadBuild`, `ParseBuild`, and the three readings of a recorded version (`fromVersion`, `mainModule`, `dependency`) |
| `stats.go` | `StatsValue`, `DistributionValue` + `Count` / `Quantile`, `ReadStats`, the runtime/metrics names, `lastGC`, `runtimeCPU` |
| `cputime_unix.go` | `cpuTime` from `getrusage(RUSAGE_SELF)` — user plus system, every thread |
| `cputime_other.go` | `cpuTime` as the runtime's estimate, flagged — Windows, Plan 9, wasm |

## Why-this-shape

- **The values live here, not in `core/proc`** (ADR 0074). No port of the proc
  domain speaks them and one mechanism produces them; hoisting them would put
  one reading of `runtime/debug` into the contract layer.
- **No error anywhere.** Not being built with module support, a metric the
  toolchain does not export, a platform without `getrusage` — none is a fault a
  caller can act on. Each is a zero field, a `false`, or `CPUEstimated`, and
  each field's comment says which.
- **A recorded version conflates three things; `ModuleValue` separates them.**
  A RELEASE (`Version`), a COMMIT (`Revision`, `Time` — what a pseudo-version
  and the main module's `vcs.*` stamp name), and a DIRECTORY (`Local`, `Dir` —
  a directory replacement, a workspace module, a main module built in its own
  tree). The downstream copy this replaces reported a pseudo-version as "no
  version" and lost the commit it named, and left a workspace module neither
  versioned nor local; both are now told. The main module's stamp beats its
  pseudo-version (a full revision against a twelve-character prefix, exact time
  against a second-granular copy), and `+dirty` — which Go 1.24 appends —
  becomes `Modified` instead of staying in the version string.
- **Measured, not assumed: what the toolchain records.** In workspace mode a
  directory replacement is recorded relative to the WORKSPACE root, not to the
  main module (`../lib` in `go.mod` read back as `./lib`), and a workspace
  module is recorded as `(devel)` with no directory at all. `Dir`'s comment
  says both.
- **Everything cumulative, on purpose.** The runtime's histograms count since
  the process started, so a p99 read here is over the process's life. A window
  is two snapshots subtracted; a snapshot that reset the runtime's counters
  would break every other reader.
- **`Quantile` reads a bucket's UPPER bound** — the honest reading of a bucketed
  histogram — and `q = 0` lands on the first bucket that holds an observation,
  not the first bucket; the downstream copy answered the empty first bucket.
- **`started` is this package's initialisation**, not the kernel's process
  start: the gap is the runtime's own bring-up. Reading `/proc/self/stat` would
  be exact on Linux and nothing elsewhere; a field that means different things
  per platform is worse than a documented approximation.
- **CPU time is the kernel's where it exists.** The runtime's estimate counts
  only threads running Go code, so it misses cgo and time blocked in the kernel;
  `CPUEstimated` says when that is what the caller got.

## Do NOT

- Add an error return. A caller has nothing to do with "this platform has no
  getrusage" except read the flag.
- Reset or window the runtime's counters here. Subtract two snapshots.
- Parse pseudo-versions by hand. `golang.org/x/mod/module` is the toolchain's
  own grammar, three forms and build metadata included.

## Verification

```sh
bazel test --config=race //internal/service/proc/self:self_test
cd internal/service && GOWORK=off go test -race ./proc/self/
```
