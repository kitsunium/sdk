<!-- updated: 2026-10-03T13:06:04Z -->
# internal/service/observe/profiling/

## Purpose

The running process's own profiles (ADR 0121): `CaptureCPU` over a bounded
window and `CaptureHeap` through `runtime/pprof`; `Parse`, the pprof format
decoded with the standard library; `Fold`, the samples charged to owners the
caller's `Attribute` names, with per-owner costs, the costliest functions and a
pruned flame graph; `Goroutines` / `ParseGoroutines`, the runtime's dump read
into goroutines, and `GroupGoroutines`; `CanonicalName`. Public facade:
`pkg/v1/observe/profiling`.

Stdlib only (plus `kernel/clock` — the CPU window is a timer on it, `clock.System` from `CaptureCPU`, a `ManualClock` in `Test_captureCPU` — `kernel/errs`, the transform PORT `core/data/transform`, through which a gzipped profile is inflated, and its core, `internal/core/observe/profiling`). Code range `0.3.89.*`, declared in that core since ADR 0160 §2 together with the values this engine produces; this package imports it as `coreprofiling` and declares neither.

## Contents

| File | Role |
|---|---|
| `profiling.go` | package doc; `MaxCPUWindow`, `MaxProfileBytes`, `MaxFrames` — the bounds of the capture and the decoder, the engine's |
| `capture.go` | `CaptureCPU`, `CaptureHeap` |
| `wire.go` | the protocol-buffer wire reader: varints (a tenth byte past the 64th bit refused), length-delimited fields, skips, packed or unpacked repeated varints — every read bounds-checked |
| `parse.go` | `Parse`: the bound, gzip or raw — gzip inflated through the registered `"gzip"` transform scheme, its refusal kept `PROFILE_MALFORMED` around the library's own error (`libraryCause`) — the Profile's top-level fields through a reader table |
| `tables.go` | Sample, Label, Location, Line, Function; `resolve(frames)` |
| `resolve.go` | string, location and function indexes checked and resolved; a location's frames built once and shared; the frame budget (`spend`) |
| `fold.go` | `Fold`, `FoldConfig` and its defaults; `resolveSampleType` — the sample type a fold reads (the caller's, else the profile's default, else its last, pprof's convention) and its index — the lookup that was two methods of the profile value before the values moved to the core |
| `goroutine.go` | `Goroutines` (over `goroutinesFrom`, the writer injectable), `ParseGoroutines` and the dump parser; `readState`, the header's bracket read into a goroutine |
| `labels.go` | the labels in a dump header (the runtime's quoting) and in the counted profile (`%q`), and their matching by stack |
| `group.go` | `GroupGoroutines`, `GroupConfig`, the length-prefixed group key, the top frame |
| `canonical.go` | `CanonicalName` |
| `internal/core/observe/profiling` | the values (`ProfileValue`, `SampleTypeValue`, `SampleValue`, `FrameValue`, `FoldedValue`, `OwnerCostValue`, `FunctionCostValue`, `FlameNodeValue`, `FlameRoot`, `GoroutineValue`, `GoroutineGroupValue`) and the seven codes and sentinels of `0.3.89.*` — the core since ADR 0160 |

## Why-this-shape

- **A core with values and codes, and no port.** ADR 0121 §D1 gave this
  domain no core: one engine, the runtime's, and the attribution a PARAMETER
  (`FoldConfig.Attribute`), not a port. ADR 0160 §1 gave it one anyway, for
  the rule "every service domain has a core": `internal/core/observe/profiling`
  holds what a capture, a fold and a dump produce and the seven sentinels. The
  reasoning against a PORT still holds and is recorded there. The configs,
  their defaults and the bounds stay here — they parametrise mechanisms.
- **No protobuf dependency.** `Parse` is written from profile.proto; it was
  checked against `github.com/google/pprof/profile` out of tree on real CPU,
  heap, allocs and goroutine profiles (identical on every field it reads) and
  `FuzzParse` pins that nothing panics or reads past its buffer. The string
  table is written LAST by runtime/pprof, so nothing resolves before the end:
  `decoder` keeps raw tables and `resolver` checks every index.
- **Strict and bounded.** A malformed profile is refused, never guessed at; a
  sample must carry one value per sample type (the fold indexes them, and
  refuses a hand-built profile that does not, or a nil one); a varint's tenth
  byte carries the 64th bit alone; gzip is inflated by the transform domain's
  `"gzip"` scheme through its `BoundedDecompressor` port, asked for at most
  `MaxProfileBytes` — the bounded drain, the trailer check and the reader pool
  are that scheme's, written once (`internal/service/data/transform`), where
  this package carried a `LimitReader(MaxProfileBytes+1)` copy of its own.
- **The scheme is reached through the core port, not imported.** Importing
  `internal/service/data/transform` would be an edge from one engine to
  another for one function; `coretransform.Lookup("gzip")` is a dependency on
  the contract, and the facade `pkg/v1/observe/profiling` links the scheme by a
  blank import — as `pkg/v1/data/codec` does for its compressed frames — so
  every program built on the facade has it. A program that reaches this
  package WITHOUT it gets the transform domain's `UNKNOWN_COMPRESSOR` for a
  gzipped profile, never a silent miss; the suite links the scheme the facade's
  way, in `parse_external_test.go`. Its refusals stay this package's: an
  over-cap stream (`DECOMPRESSED_TOO_LARGE`) is `PROFILE_TOO_LARGE`, and one
  that does not decode is `PROFILE_MALFORMED` wrapping compress/gzip's own error,
  never the scheme's `GZIP_FAILED` — a plain wrap would inherit it, origin wins
  (`TestAGzipRefusalIsTheProfilesOwn`).
- **The bytes do not bound the frames.** A sample names a location by its id
  and a location stands for all its inlined lines, so a small profile naming
  one deep location over and over would expand quadratically. `resolver.spend`
  counts every frame — each location's, once, and each stack's copy — against
  `MaxFrames` BEFORE it is built, and refuses with `ProfileTooLarge`
  (`TestTheFramesAreBoundedBeforeTheyAreBuilt` drives `resolve` with a small
  budget, so the test allocates nothing large).
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
  split on every tab and read "" on linux/arm64. When the counted profile
  cannot be written the goroutines are returned without labels, never dropped.
- **The group key has no separator.** Every part — each label marked present
  or absent, the state, the top frame — is prefixed with its length, so a
  label value holding a NUL or an "=" cannot merge two groups
  (`TestNoValueMergesTwoGroups`).

## Do NOT

- Add a gate around the CPU profiler: two gates disagree, the runtime's is the
  one that counts.
- Round, or convert units, in `Fold`.
- Quote a profile or a dump in an error: both describe the process's code and
  what it was doing.
- Assert exact heap bytes in a test. The heap profile is sampled.

## Verification

```sh
bazel test --config=race //internal/service/observe/profiling:profiling_test
cd internal/service && GOWORK=off go test -race ./observe/profiling/
cd internal/service && GOWORK=off go test -run=NONE -fuzz=FuzzParse -fuzztime=30s ./observe/profiling/
```

The CPU tests do not call `t.Parallel`: the process has one CPU profiler.
