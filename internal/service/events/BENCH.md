<!-- generated from internal/service/events/events_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./events/` to refresh -->
# Benchmarks — `internal/service/events`

The in-process event bus (ADR 0053). These benchmarks exist to price three
claims the package makes in prose, all three of which sound right and are
routinely wrong:

1. **The `any` + type-assertion erasure is what a heterogeneous bus costs in
   Go, and that cost is small.** Named, because the alternative — a generic
   `Bus[E]` — carries exactly one event type, and "small" is not an argument
   until it is a number.
2. **A dispatch with no failure allocates nothing.** The `DispatchValue` is
   five scalars for exactly this reason.
3. **Publishing into a bus nobody listens to is cheap enough to do
   unconditionally.** A publisher that has to guard its own `Publish` calls
   has not been relieved of anything.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `e01714c` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkDirectCall-8                     490153503     2.455 ns/op      0 B/op   0 allocs/op
BenchmarkPublish_OneTypedListener-8        17506525    72.56  ns/op      0 B/op   0 allocs/op
BenchmarkPublish_OneErasedListener-8       17428030    66.06  ns/op      0 B/op   0 allocs/op
BenchmarkPublish_EightTypedListeners-8      4673869   250.4   ns/op      0 B/op   0 allocs/op
BenchmarkPublish_FreshEvent-8              12126836    96.59  ns/op     16 B/op   1 allocs/op
BenchmarkPublish_NoListener-8              36408069    32.86  ns/op      0 B/op   0 allocs/op
BenchmarkPublish_HaltAtFirst-8             12136881    96.83  ns/op      0 B/op   0 allocs/op
BenchmarkPublish_OneListenerFails-8         1843395   596.3   ns/op    384 B/op   7 allocs/op
BenchmarkSubscribe-8                         639433  1709     ns/op   1608 B/op   9 allocs/op
```

## Claim 1 — what the type assertion costs

`BenchmarkPublish_OneTypedListener` registers through `On[E]`: the listener is
a `func(context.Context, benchEvent) error`, wrapped by `erase` in a
`func(context.Context, any) error` that asserts on every delivery.
`BenchmarkPublish_OneErasedListener` registers the erased shape **directly**
through `Bus.Subscribe` and never converts. The two listener bodies are the
same statement (`sink++`), so the only difference between the two dispatches
is the assertion.

A single `-benchtime=1s` pair is too close to read, so the comparison was run
ten times at `-benchtime=2s`:

| Path | mean | min | max |
|---|---|---|---|
| erased, no assertion | 67.44 ns | 66.46 | 68.66 |
| typed, one assertion | 69.85 ns | 68.40 | 71.64 |

**The assertion costs 2.4 ns per listener call — 3.5 % of a one-listener
dispatch.** The ranges barely touch (68.66 vs 68.40), so the delta is a real
signal rather than noise, and it is one comparison of two type pointers, which
is what it should be.

That is the price of a bus that carries several event types. A generic
`Bus[E]` would remove it entirely and would also mean one bus per event type,
which is a typed channel with extra steps — see ADR 0053 §D3. 2.4 ns is not a
number that changes that decision, which is the point of measuring it.

## Claim 2 — a dispatch that does not fail does not allocate

Every `0 allocs/op` row above is a real dispatch: the atomic snapshot load,
the map lookup, the ordered walk, the panic guard per listener, and the
`DispatchValue` returned by value. `errors.Join` of an empty slice is a
genuine nil and the failure slice stays nil until something fails, so the
happy path never builds one.

The failing path is priced separately and honestly:

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| one listener, succeeds | 72.56 | 0 | 0 |
| one listener, fails | 596.3 | 384 | 7 |

Those seven allocations are the verdict (`ListenerFailed` wrapped with two
fields), the join of verdict-and-cause, and the outer aggregate. They exist
only when something went wrong, which is the side of the trade that can afford
them.

### The one cost this package cannot remove

`BenchmarkPublish_FreshEvent` builds a new event per iteration instead of
publishing a loop-invariant one, which stops the compiler hoisting the
interface conversion out of the loop:

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| loop-invariant event (hoisted) | 72.56 | 0 | 0 |
| fresh event per publish | 96.59 | 16 | 1 |

**A real call site pays 16 B and one allocation to box its event into the
`any` that `Publish` takes**, and the other rows in this table hide it. Saying
so is the point: a reader who takes `0 allocs/op` at face value would size a
hot publisher wrong. The allocation belongs to the caller's value, not to the
bus, and no signature this domain could offer removes it while keeping one bus
for many types.

## Claim 3 — publishing to nobody is cheap

`BenchmarkPublish_NoListener` publishes a type this bus has no subscription
for: **32.9 ns, zero allocations.** That is `reflect.TypeOf`, one atomic load
and one map miss. Publishing unconditionally is affordable, which is what
makes "a bus with no listener is legitimate" (ADR 0031, ADR 0053 §D6) a usable
rule rather than a slogan.

## Per-listener cost, and what the halt saves

| listeners run | ns/op | per listener |
|---|---|---|
| 0 | 32.86 | — |
| 1 | 72.56 | 39.7 |
| 8 | 250.4 | 27.2 |

The fixed cost of a dispatch is ~33 ns; each additional listener is ~27 ns,
which is the panic-guarded call plus the outcome classification.

`BenchmarkPublish_HaltAtFirst` registers nine listeners and halts on the
first: **96.8 ns instead of 250.4 ns.** The eight that were skipped cost
nothing, which is what "stopped propagation" has to mean to be worth having.
The halting dispatch is dearer than a one-listener dispatch (96.8 vs 72.6)
because a non-nil outcome walks the error chain twice — once to rule out a
panic, once to recognise the control sentinel — and that is the price of
never reading a crash as a decision.

## Membership churn

`BenchmarkSubscribe` does a full `On` + `Off` cycle against an eight-listener
index: **1709 ns, 1608 B, 9 allocs.** That is the copy-on-write trade, stated
rather than hidden — the read path is free because the write path rebuilds the
type index and the affected listener slice. A bus is wired once and published
to for the life of the process, so this is the right side to pay on; a caller
who subscribes per request has the number they need to know they are doing
something the primitive was not shaped for.
