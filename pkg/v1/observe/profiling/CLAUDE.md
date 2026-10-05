# pkg/v1/observe/profiling/

## Purpose

Public facade over `internal/core/observe/profiling` and
`internal/service/observe/profiling` (ADR 0121): the running
process's CPU over a bounded window, its live heap, and its goroutines —
decoded with the standard library, folded onto the owners you name, grouped.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CaptureCPU(ctx, window)` | func | a `*Profile`; one CPU profiler per process, so a second capture is `ProfilerBusy` |
| `CaptureHeap()` | func | the live heap, after a collection |
| `Parse(data)` | func | a pprof profile, gzipped or not, bounded by `MaxProfileBytes` read and `MaxFrames` built |
| `Fold(p, cfg)` | func | a `Folded`: per-owner costs, top functions, flame graph, in the profile's unit |
| `Goroutines()`, `ParseGoroutines(dump)` | func | the process's goroutines; any dump, never failing |
| `GroupGoroutines(gs, cfg)` | func | groups by labels, state and top frame, largest first |
| `CanonicalName(name)` | func | one spelling per function |
| `Profile`, `SampleType`, `Sample`, `Frame` | alias | the decoded profile |
| `FoldConfig`, `Folded`, `OwnerCost`, `FunctionCost`, `FlameNode` | alias | the fold |
| `Goroutine`, `GroupConfig`, `GoroutineGroup` | alias | the goroutine view |
| `MaxCPUWindow`, `MaxProfileBytes`, `MaxFrames`, `FlameRoot`, `Default*` | const | |
| `Code*` (7) and the sentinels | const / var | `0.3.89.1`–`0.3.89.7`; each code declared on its own, the sentinels in one `var` block |

The values and the sentinels alias the core, `internal/core/observe/profiling`,
where ADR 0160 put them; `FoldConfig`, `GroupConfig`, the bounds and the
defaults alias the engine, whose parameters they are; the functions delegate
to the engine. An alias points at the layer that owns its symbol (ADR 0074).
There is no port: one engine, and the attribution is a function you pass.

The facade also blank-imports `internal/service/data/transform`: `Parse` — and
so every capture — inflates the runtime's gzipped profiles through the
transform domain's `"gzip"` scheme, which that import registers (with `flate`
and `zlib`), as `pkg/v1/data/codec` does. The package doc says so, because a
program registering a `"gzip"` scheme of its own would meet the duplicate at
boot.

## Why-this-shape

- **The attribution is a function you pass**, because only you know what a
  sample was working for — a label you set with `pprof.Do`, or your code on
  the heap sample's stack.
- **The package doc states what is estimated** — a sampled heap, a CPU
  counted at about 100 Hz — so a test asks where the bytes are.
- **Each constant is declared on its own; the sentinels share one block.**
  gomarkdoc gives a grouped declaration ONE anchor — the first name's — so a
  constant declared alone gets its own; the sentinels cannot, since
  KTN-VAR-GROUP wants one `var` block, so the package doc names them in plain
  text rather than link every one of them to `WindowInvalid`.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`profiling.go` (ADR 0008). Regenerate with `make docs-readme`; do not hand-edit
it.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/observe/profiling.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/observe/profiling:profiling_test
cd pkg && GOWORK=off go test -race ./v1/observe/profiling/
```
