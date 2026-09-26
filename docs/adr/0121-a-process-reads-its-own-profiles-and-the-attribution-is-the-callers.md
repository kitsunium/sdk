# ADR 0121 — a process reads its own profiles, and the attribution is the caller's

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0048](0048-sdk-metrics-otlp-json.md) (a wire format written from its document, no dependency), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (whose type a value is), [ADR 0100](0100-a-program-reads-what-it-was-built-from-and-asks-git-only-about-a-working-tree.md) (what a process says about itself), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range)

## Context

A downstream framework's developer console shows where the running product
spends its CPU and its memory and what its goroutines are doing, each charged
to the component that did it. About 740 lines, most of them general: capture
the CPU for a window and the live heap through `runtime/pprof`, decode the
pprof format, fold the samples onto owners with a flame graph and the
costliest functions, read the runtime's goroutine dump into goroutines with
their state and labels, and group them. What is the framework's own is the
attribution — which of its components a sample belongs to, from a pprof label
it set or from its component's functions on the stack. Five things were wrong
with keeping the rest there:

- **The decoder was a dependency.** It parsed profiles with
  `github.com/google/pprof/profile`, which is a module and a transitive graph
  for what profile.proto describes in eleven top-level fields.
- **The fold rounded before it handed back.** Nanoseconds were summed as
  floating-point milliseconds and each figure rounded on its own, so the owners
  and the unattributed part added up to the total only to within the rounding —
  which its own test tolerated.
- **Its heap test asked for exact bytes.** A heap profile is SAMPLED — about
  one allocation per 512 KiB is recorded and scaled back up — and the test
  failed on a run that estimated 5.8 MiB of 8 held.
- **Its label fallback failed on Linux.** With the labels off in the dump's
  headers (`GODEBUG=tracebacklabels=0`) it matched them from the counted
  goroutine profile, whose columns tabwriter aligns with TABS: a frame whose
  address is shorter than the widest carries an empty column, and splitting on
  every tab read its function as "". On darwin the addresses have one width, so
  its test passed there; on linux/arm64 the runtime's and the program's differ,
  and no goroutine got its labels.
- **The dump reader was untested outside its product,** while a goroutine
  dump's format is the runtime's and changes: Go 1.27 prints the labels in the
  headers (`tracebacklabels`), in a quoting of its own, and a Windows path
  carries a colon before its line number.

## Decision

A new service package, `internal/service/profiling`, published as
`pkg/v1/profiling`. Code range `0.3.89.*` (`0x00_03_59_*`), seven codes.

### D1 — a service package with no core counterpart

There is one engine — the runtime's profilers — and no port a second
implementation would satisfy. The attribution is a PARAMETER of the fold, a
function the caller passes, not a contract the SDK declares. The values are the
engine's (ADR 0074), as for `redact`.

### D2 — captures, bounded, one CPU profiler at a time

`CaptureCPU(ctx, window)` samples for a window in (0, `MaxCPUWindow` = 5 min],
refusing anything else with `WindowInvalid` (400): a longer profile is not more
precise, only larger and longer to hold the process's ONE CPU profiler. While
that profiler runs — another capture, or anyone's `pprof.StartCPUProfile`,
net/http/pprof's included — a capture is refused with `ProfilerBusy` (409)
rather than queued. A context that ends first stops the profiler and returns
`CaptureCanceled` (503) joined with the context's error, and NO profile: a
partial window would read as the whole one. `CaptureHeap` collects garbage
first, so the in-use figures describe what is reachable now.

### D3 — the pprof format read with the standard library

`Parse` decodes profile.proto from its definition: a protocol-buffer wire
reader of about a hundred lines of code (varints, length-delimited fields, the
skip of a field it does not read) and the eleven Profile fields it needs, six
hundred lines in all; repeated
scalars packed or not, as runtime/pprof writes both. It is STRICT: every
string, function and location index is checked, a sample must carry one value
per sample type, and anything else is `ProfileMalformed` naming the part — never
quoting the input. It is BOUNDED: `MaxProfileBytes` (64 MiB), before reading
and once inflated (`ProfileTooLarge`). Each location's frames are resolved once
and shared by every sample through it; an inlined call is a frame of its own,
innermost first, and an unsymbolized location is a frame with an address and
no name.

It was checked out of tree against `github.com/google/pprof/profile` on real
CPU, heap, allocs and goroutine profiles from go1.27.1: every sample type,
period, time, duration, default sample type, value, frame (function, file,
line, start line), label and numeric label identical. It is fuzzed.

### D4 — the fold adds up exactly, in the profile's own unit

`Fold(profile, FoldConfig)` sums one sample type — the profile's default, else
its last, which is `cpu` for a CPU profile and `inuse_space` for a heap — and
charges each sample to the owner `Attribute` names, "" for nobody. The result is
in the sample type's unit, never rounded: `Total` is exactly `Unattributed` plus
every owner's `Value`, and presentation is the caller's. Flat is the innermost
named frame, cumulative counts a recursive function once per sample, rankings
go by flat then cumulative then name, and the flame graph climbs from the
outermost frame, bounded in depth and pruned below a share of the total. The
defaults are the framework's own (25 functions, 8 per owner, 0.5 %, 64 deep),
taken when a field is not positive — or, for the share, outside (0, 1) or NaN.

### D5 — a dump reader that never fails, and a grouping

`ParseGoroutines` reads a dump from anywhere — the goroutine profile at debug
2, `runtime.Stack`, a crash, a SIGQUIT: the id, the state with the runtime's
momentary flags stripped (`(scan)`, `(leaked)`, `(durable)`) but its meaningful
parentheses kept (`chan receive (nil chan)`), the minutes blocked, the thread
lock, the labels in the runtime's quoting, each frame's function, file and line
— the line after the LAST colon, so `C:/…/file.go:12` keeps its drive — and the
creator. A header it does not understand skips its block; a line it does not
know is ignored. `Goroutines` takes the process's own dump and, when the
headers carry no labels (`GODEBUG=tracebacklabels=0`), matches them from the
counted profile by the innermost frames — reading its tab-aligned columns by
their non-empty fields. `GroupGoroutines` counts goroutines by
the labels the caller names, the state, and the top frame — the innermost one
that is not the runtime's machinery.

### D6 — one spelling per function

`CanonicalName` spells a function the same way whoever named it — the runtime's
`pkg.(*T).M`, go/types' `(*pkg.T).M`, an instantiation `pkg.F[...]`, a closure
`pkg.F.func1.2`, a wrapper `pkg.F.gowrap1`, a method value `pkg.T.M-fm` — so a
frame meets the function a static analysis found. It is how a caller writes an
`Attribute` that charges heap samples by their stack.

### D7 — what is estimated is said

The package doc says it first: a heap profile is sampled and a CPU profile
counts about a hundred samples a second; both say where the cost is with
confidence and how much only approximately. The suite asks where the bytes are
— sixteen megabytes held by a named function must read as at least four —
never how many exactly.

## Consequences

- The framework keeps its node attribution (its function-to-node table from
  the graph, the module-relative source paths), its routes and its model, and
  deletes the capture, the decoding, the folding and the dump reading — and its
  dependency on `github.com/google/pprof`. Its CPU fold's `Attribute` reads its
  `kit_node` label, its heap fold's walks the stack with `CanonicalName`; it
  converts nanoseconds to its milliseconds once, at the end; its goroutine view
  is `GroupGoroutines` with its node and loop label keys, 300 groups, 32
  frames.
- Its heap test asks where the bytes are.

## Breaking changes

None. `profiling` is a new package in this change set.

## Alternatives considered

- **Depend on `github.com/google/pprof/profile`.** It is well made and it is a
  module graph in the service layer for a format the SDK reads in six hundred
  lines, checked against it; ADR 0048 drew the same line for OTLP.
- **Round in the fold.** The caller knows its unit and its precision; a fold
  that rounds cannot promise that its parts add up to its total.
- **Queue a second CPU capture.** The caller cannot tell a queued capture from
  a slow one, and the runtime's refusal is already the truthful answer.
- **Return the partial profile on cancellation.** A caller would have to check
  the duration to know it is not the window it asked for; one that wants a
  shorter window asks for one.
- **A `core/profiling` port.** Nothing implements it twice.

## Deferred

- **Block, mutex and allocs captures.** `Parse` and `Fold` read them already;
  capturing needs the rates set, which is the process's policy.
- **Symbolization** of unsymbolized frames (cgo, stripped binaries).
- **Execution traces** (`runtime/trace`) — another format and another question.

## Verification

- `internal/service/profiling/parse_external_test.go` — a hand-built profile
  exercising both repeated encodings, inlined frames, an unsymbolized
  location, labels, comments and skipped fields; gzip and raw alike; each
  malformation refused by code; both bounds; `FuzzParse`.
- `internal/service/profiling/fold_external_test.go` — exact sums, recursion,
  rankings and ties, the flame's root, order, pruning and depth, the sample type
  default, a missing one, no attribution.
- `internal/service/profiling/capture_external_test.go` — a labelled burner
  charged by its label with its source located; the one profiler, the window
  bounds, a cancellation that frees the profiler; the heap found by its stack.
- `internal/service/profiling/goroutine_external_test.go` — a dump with every
  construct (quoted labels, minutes, the thread lock, flags, Windows paths,
  elided frames, an unparsable header); the live process with labels printed in
  the headers and matched from the profile.
- `internal/service/profiling/group_external_test.go` — the grouping key, its
  order and bounds, and `CanonicalName`.
- `internal/service/profiling/labels_internal_test.go` — a counted profile as
  linux/arm64 writes it, empty alignment columns included, and the matching of
  its labels one goroutine per count, each with its own map.

## References

- [profile.proto](https://github.com/google/pprof/blob/main/proto/profile.proto) — the format, field by field.
- [Protocol Buffers encoding](https://protobuf.dev/programming-guides/encoding/) — varints, wire types, packed repeated fields.
- [`runtime/pprof`](https://pkg.go.dev/runtime/pprof) — the profilers, the labels, the goroutine profile's debug levels.
- [`runtime` — GODEBUG tracebacklabels](https://pkg.go.dev/runtime#hdr-Environment_Variables) — labels in the dump's headers.
