# pkg/v1/profiling/

## Purpose

Public facade over `internal/service/profiling` (ADR 0121): the running
process's CPU over a bounded window, its live heap, and its goroutines —
decoded with the standard library, folded onto the owners you name, grouped.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CaptureCPU(ctx, window)` | func | a `*Profile`; one CPU profiler per process, so a second capture is `ProfilerBusy` |
| `CaptureHeap()` | func | the live heap, after a collection |
| `Parse(data)` | func | a pprof profile, gzipped or not, bounded by `MaxProfileBytes` |
| `Fold(p, cfg)` | func | a `Folded`: per-owner costs, top functions, flame graph, in the profile's unit |
| `Goroutines()`, `ParseGoroutines(dump)` | func | the process's goroutines; any dump, never failing |
| `GroupGoroutines(gs, cfg)` | func | groups by labels, state and top frame, largest first |
| `CanonicalName(name)` | func | one spelling per function |
| `Profile`, `SampleType`, `Sample`, `Frame` | alias | the decoded profile |
| `FoldConfig`, `Folded`, `OwnerCost`, `FunctionCost`, `FlameNode` | alias | the fold |
| `Goroutine`, `GroupConfig`, `GoroutineGroup` | alias | the goroutine view |
| `MaxCPUWindow`, `MaxProfileBytes`, `FlameRoot`, `Default*` | const | |
| `Code*` (7) and the sentinels | const / var | `0.3.89.1`–`0.3.89.7` |

All types alias the service: there is no port, and the values are the
engine's (ADR 0074).

## Why-this-shape

- **The attribution is a function you pass**, because only you know what a
  sample was working for — a label you set with `pprof.Do`, or your code on
  the heap sample's stack.
- **The package doc states what is estimated** — a sampled heap, a CPU
  counted at about 100 Hz — so a test asks where the bytes are.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`profiling.go` (ADR 0008). Regenerate with `make docs-readme`; do not hand-edit
it.

## Verification

```sh
bazel test --config=race //pkg/v1/profiling:profiling_test
cd pkg && GOWORK=off go test -race ./v1/profiling/
```
