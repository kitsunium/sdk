<!-- generated from pkg/v1/trace/trace_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=500ms -count=3 ./v1/trace/` and take medians -->
# Benchmarks — `pkg/v1/trace`

Distributed tracing on the OpenTelemetry model (ADR 0051). `Start`/`End` is what
a codebase pays **per instrumented call site**, so the question these answer is
how freely a team can instrument — and the most useful row is the one for spans
nobody records.

## Sampling halves the cost. It does not remove it.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Start` + `End`, sampled | 728.4 | 432 | **3** |
| `Start` + `End`, **not** sampled | 403.5 | 176 | **3** |
| `Ratio(0.1)` — 10 % of spans recorded | 454.1 | 201 | 3 |

A span the tracer decided not to record still costs **403 ns and three
allocations**. At a 1 % sampling ratio, that is what 99 % of spans cost, so it
is the number that actually governs a traced service's overhead — not the
sampled row.

`Ratio(0.1)` at 454.1 ns sits between the two exactly where a 10 % mix should
put it (0.1 × 728 + 0.9 × 403 = 435), which is how these three rows check each
other. The sampling decision itself is the ~19 ns of residue.

## Why the unsampled path allocates — and why it is not waste

`go tool pprof -sample_index=alloc_objects` on the unsampled path:

| | share of allocated objects |
|---|---:|
| `context.WithValue` + the SDK's `contextWithValue` | **66 %** |
| the span value in `Tracer.Start` | 33 % |

Two of the three allocations are **the derived context**, and they are
load-bearing rather than overhead. A span that is not recorded must still carry
its `SpanContext`, because:

- a nested `Start` finds its parent through the context, and a broken chain
  would re-root the trace;
- `Inject` must still emit a valid `traceparent` with the sampled flag **clear**
  — W3C Trace Context propagates the decision, and a downstream service that
  received nothing would make its own, splitting one request into two traces.

So "not sampled" means *not recorded*, never *not propagated*. The 403 ns is
what propagation costs, and no sampler can remove it.

**Which is the actionable rule**: sampling controls what is stored and shipped,
not what is spent at the call site. Instrumenting a 50 ns function is 8× its
cost even at a 0 % ratio; instrumenting a 5 ms database call is 0.008 %.

## Attributes, and the parts of a span

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Start`+`End`, sampled, no attributes | 728.4 | 432 | 3 |
| `Start`+`End`, sampled, three attributes | 1 069.0 | 720 | 5 |
| `SetAttrs`, three attributes, mid-span | 320.6 | 144 | 1 |
| `AddEvent`, name + three attributes | 531.0 | 518 | 1 |
| `SpanContext()` | **5.7** | 0 | **0** |

Three attributes at `Start` cost **341 ns and two allocations** — the slice and
its copy. Adding them later with `SetAttrs` costs 320.6 ns and one allocation,
so where they go is a readability choice rather than a performance one.

`AddEvent` at 531 ns is the most expensive per-call operation here; it records a
timestamped point, so an event per loop iteration is not free.

`SpanContext()` is 5.7 ns and allocates nothing — pinned as a field read,
because a propagator and the logger's trace correlation (ADR 0062) both call it
on their hot paths.

## Nesting is cheaper per span than a lone root

Three levels — handler → service → repository, which is one HTTP request —
cost 2 707 ns, or **902 ns per span**, against 728.4 ns for one root span alone.
The per-span figure is higher because each level derives another context; the
root's own extra work (minting a new trace identifier) is amortised across the
three.

The practical reading: a request producing three spans costs about 2.7 µs of
tracing, which against any real HTTP handler is noise.

## Reproducibility envelope

> **Numbers vary across machines** — and this file learned that the hard way. A
> first single-run pass on a loaded box reported `Ratio(0.1)` as CHEAPER than
> `NeverSample`, which is arithmetically impossible for a 10 % mix. That
> contradiction is what caught the noise; the numbers here are medians of three
> and check each other, as the sampling row above shows. **Publish a benchmark
> whose rows disagree with each other and you publish noise.**
>
> The allocation column was identical across every run, loaded or not.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | dd900cd |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=500ms -count=3`, medians |

## Results (medians of three)

```
BenchmarkStartEnd_Sampled-8                     728.4 ns/op      432 B/op     3 allocs/op
BenchmarkStartEnd_NotSampled-8                  403.5 ns/op      176 B/op     3 allocs/op
BenchmarkStartEnd_SampledWithAttrs-8           1069.0 ns/op      720 B/op     5 allocs/op
BenchmarkStartEnd_Nested-8                     2707.0 ns/op     1296 B/op     9 allocs/op
BenchmarkSpan_SetAttrs-8                        320.6 ns/op      144 B/op     1 allocs/op
BenchmarkSpan_AddEvent-8                        531.0 ns/op      518 B/op     1 allocs/op
BenchmarkSpan_SpanContext-8                       5.7 ns/op        0 B/op     0 allocs/op
BenchmarkSampler_Ratio-8                        454.1 ns/op      201 B/op     3 allocs/op
```
