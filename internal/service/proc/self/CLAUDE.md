# internal/service/proc/self/

## Purpose

What the running process is DOING (ADR 0100): its runtime state (`stats.go`,
`cputime_*.go`). Every other `proc` package acts on another process — spawns,
signals, reaps, limits a child; this one only reads, and only the process it
runs in. Public facade: `pkg/v1/proc/process` (`Self`, and the `Stats` /
`Distribution` aliases).

What the binary was BUILT from was here too and is now
`internal/service/proc/buildinfo` (ADR 0172): reading a build must not import
`runtime/metrics`, whose initialisation every process importing it pays at
start — about 0.03 ms of a status line's every render, for a figure it never
reads.

Stdlib only.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
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
- Read the build here. It is `internal/service/proc/buildinfo`'s, apart from
  `runtime/metrics` on purpose (ADR 0172).

## Verification

```sh
bazel test --config=race //internal/service/proc/self:self_test
cd internal/service && GOWORK=off go test -race ./proc/self/
```
