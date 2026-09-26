# internal/service/profiling/

## Purpose

The running process's own profiles (ADR 0121): `CaptureCPU` over a bounded
window and `CaptureHeap` through `runtime/pprof`; `Parse`, the pprof format
decoded with the standard library; `Fold`, the samples charged to owners the
caller's `Attribute` names, with per-owner costs, the costliest functions and a
pruned flame graph; `Goroutines` / `ParseGoroutines`, the runtime's dump read
into goroutines, and `GroupGoroutines`; `CanonicalName`. Public facade:
`pkg/v1/profiling`.

Stdlib only (plus `kernel/errs`). Code range `0.3.89.*`.

## Contents

| File | Role |
|---|---|
| `profiling.go` | package doc; `MaxCPUWindow`, `MaxProfileBytes` |
| `capture.go` | `CaptureCPU`, `CaptureHeap` |
| `profile.go` | `ProfileValue`, `SampleTypeValue`, `SampleValue`, `FrameValue`; the default sample type |
| `wire.go` | the protocol-buffer wire reader: varints, length-delimited fields, skips, packed or unpacked repeated varints — every read bounds-checked |
| `parse.go` | `Parse`: the bound, gzip or raw, the Profile's top-level fields through a reader table |
| `tables.go` | Sample, Label, Location, Line, Function; `resolve` |
| `resolve.go` | string, location and function indexes checked and resolved; a location's frames built once and shared |
| `fold.go` | `Fold`, `FoldConfig` and its defaults, `FoldedValue`, `OwnerCostValue`, `FunctionCostValue`, `FlameNodeValue`, `FlameRoot` |
| `goroutine.go` | `GoroutineValue`, `Goroutines`, `ParseGoroutines` and the dump parser |
| `labels.go` | the labels in a dump header (the runtime's quoting) and in the counted profile (`%q`), and their matching by stack |
| `group.go` | `GroupGoroutines`, `GroupConfig`, `GoroutineGroupValue`, the top frame |
| `canonical.go` | `CanonicalName` |
| `codes.go` / `errors.go` | `0.3.89.*`, seven codes |

## Why-this-shape

- **No core package.** One engine, the runtime's; the attribution is a
  PARAMETER (`FoldConfig.Attribute`), not a port; the values are the engine's
  (ADR 0074).
- **No protobuf dependency.** `Parse` is written from profile.proto; it was
  checked against `github.com/google/pprof/profile` out of tree on real CPU,
  heap, allocs and goroutine profiles (identical on every field it reads) and
  `FuzzParse` pins that nothing panics or reads past its buffer. The string
  table is written LAST by runtime/pprof, so nothing resolves before the end:
  `decoder` keeps raw tables and `resolver` checks every index.
- **Strict and bounded.** A malformed profile is refused, never guessed at; a
  sample must carry one value per sample type (the fold indexes them); gzip is
  read through `LimitReader(MaxProfileBytes+1)` and its trailer checked.
- **The fold never rounds.** `Total == Unattributed + Σ Owners[i].Value`
  exactly; units are the caller's to convert.
- **The CPU profiler is the runtime's, and there is one.** `CaptureCPU` does
  not keep a gate of its own: `pprof.StartCPUProfile` refusing IS the busy
  verdict, whoever holds it. A cancelled window returns no profile.
- **A dump parser that never fails.** Its input is text a runtime of any
  version wrote; an unknown header skips its block, an unknown line is ignored.
  State flags the runtime appends for the moment — `(scan)`, `(leaked)`,
  `(durable)` — are stripped; parentheses that are part of the wait reason —
  `chan receive (nil chan)` — are kept. The line number follows the LAST colon,
  for Windows drives.
- **Labels either way.** Go 1.27 prints labels in the headers
  (`tracebacklabels=1`, its default); with them off, `Goroutines` matches the
  counted profile's labelled records by their innermost twelve frames, one
  goroutine per count — best effort, since the two dumps are taken one after
  the other. `TestLiveGoroutinesCarryTheirLabelsEitherWay` runs both. The
  counted profile's columns are aligned by tabwriter, so a frame after a
  shorter address has EMPTY columns: `appendCountFrame` reads non-empty fields
  (`TestTheCountedProfileReadsPastItsAlignmentTabs`) — the copy this replaces
  split on every tab and read "" on linux/arm64.

## Do NOT

- Add a gate around the CPU profiler: two gates disagree, the runtime's is the
  one that counts.
- Round, or convert units, in `Fold`.
- Quote a profile or a dump in an error: both describe the process's code and
  what it was doing.
- Assert exact heap bytes in a test. The heap profile is sampled.

## Verification

```sh
bazel test --config=race //internal/service/profiling:profiling_test
cd internal/service && GOWORK=off go test -race ./profiling/
cd internal/service && GOWORK=off go test -run=NONE -fuzz=FuzzParse -fuzztime=30s ./profiling/
```

The CPU tests do not call `t.Parallel`: the process has one CPU profiler.
