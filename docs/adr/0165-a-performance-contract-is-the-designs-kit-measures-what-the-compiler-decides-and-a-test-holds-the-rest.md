# ADR 0165 — a performance contract is the design's: kit measures what the compiler decides, and a test holds the rest

- **Status**: Accepted
- **Date**: 2026-10-05
- **Deciders**: SDK maintainers (the owner's decision: stage 5 of "kit regenerates the Go SDK from its design, aiming at the most performant code possible")
- **Amends**: [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) (what `budgets` holds and what kit writes from it)
- **Related**: [ADR 0010](0010-kernel-recycler-primitive.md), [ADR 0011](0011-kernel-snapshot-primitive.md), [ADR 0062](0062-logger-trace-correlation.md), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) §4, CLAUDE.md rules 9 and 12

## Context

ADR 0163 gave `design/sdk.yaml` a `budgets` section, and kit check held it
the only way it could without running the SDK's code: `inline: true` by the
compiler's `-m` report, and `allocs: N` by checking that a target of the
race-off allocation lane covered the function's package. It measured nothing
of the second. The allocation claims themselves lived in about 85
`testing.AllocsPerRun` calls and a dozen hand-rolled malloc totals across 33
files, each with its own probe, its own runs, its own warm-up and its own
reading of the counter — and the contract a reader looks for ("`Compare`
allocates nothing", "an emit allocates at least once") was a sentence in a
test's doc comment, not a fact of the design.

Three of those probes had already shown why the shape of the measurement is
part of the contract: `testing.AllocsPerRun` ends in an integer division, so
a slice that doubles — the shape of every "how many are in flight?"
instrument — allocates 10 times over 1 000 calls and reads as 0
(recycler, snapshot, the logger's trace extraction); and the runtime builds a
type switch's cache lazily, on about one miss in 1024, so a total counted
over a short window catches an allocation no call makes (the logger's
fan-out guard, one run in fifteen).

## Decision

### 1. A budget is a function's performance contract, in two halves

`budgets[]` keeps `fn` (a go: id) and grows, in kit's library dialect
(platform ADR 0010's amendment of 2026-10-05,
[kitsunium/platform#56](https://github.com/kitsunium/platform/pull/56)):

- **what the compiler decides** — `inline: true`, `inlineCost: N` (the most
  the inlining cost may be) and `escapes: [names]` (the named parameters,
  the receiver by its name, that may leak to the heap or leak their content;
  `[]` says none may);
- **what a call costs** — `allocs: N` (at most N a call), `allocsMin: N` (at
  least N, a floor) and `bench: {parallel, nsop: {maxDelta: 3%, vs: base}}`,
  held through a `fixture`.

### 2. kit check measures what the compiler decides, and runs no SDK code

`kit check` builds each budgeted function's package with
`go build -gcflags=<package>=-m=2` on the host cell — `-m=2` because it
counts the whole body where `-m` stops at the budget of 80 — and reads
whether it inlines, its cost (or the compiler's reason), and which
parameters leak. A breach names the measured number; every budget's
function is printed either way (`perf go:…Pack (…/code.go:57): inlines,
cost 18`). A parameter that only flows to a result does not count. A
generic function, compiled per instantiation, and an interface's method,
which has no body, cannot be asked what the compiler decides.

### 3. A hand-written fixture sets the call up; kit gen writes the test

A budget's `fixture: X` names `func perfX(tb testing.TB) func()` in its
package's `perf_fixtures_test.go` (`//go:build !race`), which sets one call
up and returns it; kit check holds it to that shape and to naming the
budget's function. kit gen writes `perf_gen_test.go` beside it, under the
kit header and `//go:build !race`:

- a test per allocation bound, `TestPerfAllocsX`, counting the **total**
  mallocs of 30 000 calls after 30 000 warm-up calls, GOMAXPROCS pinned to 1
  and the collector off — the warm-up pays the runtime's lazy caches outside
  the window (left unbuilt with a probability near 2e-13), and the window is
  as long as the warm-up so a slice that doubles, which the warm-up grew
  too, crosses a growth step inside it;
- a benchmark per `bench`, `BenchmarkPerfX`, from GOMAXPROCS goroutines when
  `parallel`.

A budget may name an interface's method (`clock.Clock.Now`,
`ring.Queue[...].TryWrite`) — what a call through the port costs, the
fixture calling one implementation — for what a call costs only; that is
how `System`'s reads and the ring's two verbs, whose concrete types are
unexported, are contracts at all.

### 4. Every race-off file kit writes runs in the lane

kit gen keeps the last section of `tools/alloc-lane-targets.txt`, between
`# BEGIN kit gen` and `# END kit gen`, naming the test target of every
package whose budgets name a fixture; the lines above it stay hand-written
and are kept byte for byte, and `kit gen -check` fails on a hand edit inside
it. Rule 12 holds by construction: `check-alloc-lane-coverage.sh` and
`kit check` agree that every `perf_gen_test.go` is covered.

### 5. A number is never generated

kit writes no BENCH.md and no benchmark result. `make bench` remains the
only writer of a BENCH.md (rule 9), and a generated benchmark's ns/op is read
only as a benchstat delta against the change's base (`nsop.maxDelta`,
`vs: base`) — a machine's absolute ns/op is that machine's.

### 6. The converted contracts, each bound kept

| Was | Is (budget, fixture) | Bound |
|---|---|---|
| kernel zero-alloc gate (`internal/kernel/zeroalloc_gate_integration_test.go`, `AllocsPerOp == 0`) — errs | `errs.Pack`, `(*Error).Code/Reason/Public/Private`, `HasCode` | `allocs: 0` |
| the same — clock, ring, buffer | `clock.Clock.Now`, `Clock.Since`; `ring.Queue[...].TryWrite`, `TryRead` (both paths each); `buffer.Get` | `allocs: 0` |
| recycler `TestZeroAllocInvariant` | `(*Pool[...]).Get`, `(*CappedPool[...]).Get` (its buffer arm is `buffer.Get`'s) | `allocs: 0` |
| snapshot `TestZeroAllocInvariant` | `(*Value[...]).Load`, `Store`, `Swap` | `allocs: 0` |
| semver `TestReadingsAllocateNothing` | `IsValid`, `Compare`, `Prerelease`, `IsPseudoVersion`, `PseudoVersionRev`, `PseudoVersionTime` (every case of a function in its one call) | `allocs: 0` |
| logger `TestV116BuildSendAllocatesOnePerEmit` (`AllocsPerRun >= 1`) | `logger.Build`, fixture `BuildSend` | `allocsMin: 1` |
| logger `TestT34TraceContextExtractionIsAllocationFree` | `logger.TraceContextFromContext` (hit and miss in one call) | `allocs: 0` |

Each bound is the old one; the measurement is the same total or stricter (a
total of 0 over 30 000 calls where `AllocsPerOp == 0` tolerated fewer
allocations than iterations). New contracts join them where nothing held the
claim: the codecs' `scratch` — a warm buffer or reader round trip allocates
nothing, a detach exactly once (`allocs: 1, allocsMin: 1`); the five errs
accessors' `inline: true` is kept, and `Pack` and the four `*Error` field
reads gain `inline: true` and `escapes: []`; `HasCode` and `logger.Build`
gain a benchmark with `nsop: {maxDelta: 3%, vs: base}`.

The tests that stay hand-written are the ones that are not a fixed bound on
an exported entry point: differential guards (the logger's fan-out width and
trace correlation, the writers' deltas against a transport-free emit),
budgets on unexported paths (websocket, sse, the outbound client, token's
split), bytes rather than counts (the decompression-bomb guard), and the
per-codec allocation tables.

## Consequences / Semantics

- **What kit measured on the SDK** (go1.27.1, darwin/arm64, the host cell):
  the `*Error` field reads inline at cost 3, `Pack` at 18, `CodeOf` …
  `PrivateOf` at 73–75 and leak `err`; `HasCode` does not inline (190) and
  leaks `err`; `buffer.Get` and `scratch.AcquireBuffer` inline at 70,
  `AcquireReader` at 80 (the budget itself) and leaks `src`, `DetachBuffer`
  does not (123) and leaks `buf`; semver's readings inline at 67–78 except
  `Compare` (511) and `PseudoVersionTime` (153), every one leaking its
  string; `logger.Build` inlines at 62 and leaks `lg`;
  `TraceContextFromContext` does not (94) and leaks `ctx`.
- **Mutation-checked**: a `published` slice appended in `snapshot.Store`
  fails `TestPerfAllocsStore` at 2 allocations over 30 000 calls; storing
  the receiver of `(*Error).Code` in a package variable fails kit check's
  escapes contract (`its parameter e leaks to the heap`).
- A contract change is a design change: `kit gen` rewrites the test and the
  lane section, `kit gen -check` (and genindex's digests) catch a hand edit.
- The kernel's root test target (`//internal/kernel:kernel_test`) is gone
  with its gate; the lane's hand-written entries for recycler, snapshot and
  semver moved into kit's section.

## Breaking changes

None for a consumer: no exported symbol changes, and docs/api moves by
one package doc sentence. Inside the repository, test names move:
`TestV116BuildSendAllocatesOnePerEmit` is `TestPerfAllocsBuildSend`, the
kernel gate's and the recycler's, snapshot's and semver's
`TestZeroAllocInvariant` / `TestReadingsAllocateNothing` are
`TestPerfAllocs<Fixture>`, `TestT34TraceContextExtractionIsAllocationFree`
is `TestPerfAllocsTraceContext`, and the target `//internal/kernel:kernel_test`
is gone. A `-run` filter or a document naming the old names must follow.

## Alternatives considered

- **kit counting allocations itself at kit check.** Refused: it would run
  the project's code inside kit, which kit check never does; a test in the
  race-off lane is where the SDK already runs such code, and the
  allocation claim stays executable without kit.
- **`testing.AllocsPerRun` in the generated test.** Refused: its integer
  division reads a call allocating on some runs only as 0, which three of
  the converted tests had each demonstrated with a mutation.
- **The minimum of several windows** (what the logger's trace test did, to
  shed a stray runtime allocation). Refused for the generated test: windows
  that follow one another cross a doubling slice's growth steps in some
  windows and not others, so the minimum hides the very regression a total
  exists to see. The warm-up as long as the window pays the runtime's lazy
  caches instead, and GOMAXPROCS 1 with the collector off keeps the window
  to the call; forty consecutive runs of every generated test were green.
- **Argument lists in the design** (`bench: {args: [...]}`). Refused: an
  argument is a Go expression kit would have to type against the signature,
  and a receiver needs setting up anyway; the fixture is that set-up, in Go.
- **An absolute ns/op bound.** Refused: a machine's number is that
  machine's (rule 9); only a delta against the base is a contract.
- **`*_external_test.go` names for the fixtures.** Not taken: the generated
  file is kit's name, as `api_gen_test.go` is, and one fixture file name
  across packages is what kit's documentation and the lane section name.

## Deferred

- The benchstat comparison of `nsop.maxDelta` against the base: the
  benchmarks exist and their names are stable, but no lane runs head and
  base and judges the delta yet.
- The remaining hand-written allocation tests on unexported paths
  (websocket, sse, the outbound client, token's split) and the per-codec
  allocation tables: each needs an exported entry point or a port to hang
  a budget on.
- `inlineCost` pins: none is declared; a pin on a function that does not
  inline (HasCode, 190) would keep it from growing, and is left to the
  owner's call function by function.

## References

- kitsunium/platform#56 — kit's performance contracts, and platform ADR
  0010's amendment of 2026-10-05
- `tools/alloc-lane-targets.txt`, `scripts/pre-commit/check-alloc-lane-coverage.sh`
- CLAUDE.md rules 9 and 12
