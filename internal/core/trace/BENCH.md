<!-- generated from internal/core/trace/trace_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./trace/` to refresh -->
# Benchmarks — `internal/core/trace`

W3C Trace Context propagation (ADR 0051). `Extract` runs **once per inbound
request** on a traced service and `Inject` once per outbound call, before any of
the work the request came to do — so this is the one package in the domain where
a per-call allocation is a per-request garbage generator.

It had no benchmarks. Writing them found two allocations on that path, and they
are gone.

## What the profile found, and what it cost to fix

`BenchmarkParseTraceParent_Valid` measured **266.4 ns with 2 allocations**. A
memory profile attributed them precisely:

| source | share of allocated objects |
|---|---:|
| `strings.Split` in `parseTraceParentPrefix` | 92.63 % |
| `hex.DecodeString` in `parseTraceFlags` | 7.03 % |

Both were avoidable without changing a single decision the parser makes.

**`strings.Split` built a four-element `[]string` on every inbound request.** A
traceparent is fixed-width by specification — §3.2.4 requires *every* version to
keep the first 55 characters exactly where version 00 puts them — so the three
dashes sit at known offsets and the fields can be read in place. The offsets are
derived from the width constants rather than written as literals, so the grammar
and the arithmetic cannot drift apart. The dash check that replaces the
field-count check is *stricter*, not looser: a dash landing inside a field is
still caught, by the width check `ParseTraceID` already performs.

**`hex.DecodeString` RETURNS a fresh slice** — one heap-allocated byte per
request. Its sibling `ParseTraceID`, ten lines away, already decodes into a
fixed array with `hex.Decode`; `parseTraceFlags` now does the same. The
inconsistency was the whole bug.

| | before | after |
|---|---:|---:|
| `ParseTraceParent` (valid) | 266.4 ns · 65 B · 2 allocs | **127.1 ns · 0 B · 0 allocs** |
| `ParseTraceParent` (all-zero, a refusal) | 185.8 ns · 64 B · 1 alloc | **61.65 ns · 0 B · 0 allocs** |
| `Extract` (traceparent only) | 346.0 ns · 65 B · 2 allocs | **199.6 ns · 0 B · 0 allocs** |

Every test stayed green, including the conformance suite for §3.2.2 and §3.2.4.

## The refusal paths are cheap, and that is a security property

A public entry point is the one place a caller cannot choose its input, so a
refusal that costs far more than an acceptance is an amplification an attacker
gets for free.

- `ParseTraceParent_Malformed` (wrong-length identifier) — **10.83 ns**, and it
  never reaches the decoder: the width test comes first.
- `ParseTraceParent_AllZero` — **61.65 ns**. Higher, because §3.2.2.3 requires
  the id to be decoded before it can be judged all-zero. It is now cheaper than
  a *successful* parse was before this change.
- `Extract_Absent` — **24.86 ns**, zero allocations. The untraced request is the
  common case on any public endpoint, and it is the cheapest path here.
- `Inject_Invalid` — **12.15 ns**, zero allocations, and it writes nothing at
  all. The documented shortcut is real: an invalid context does not pay for
  formatting a header the next hop is obliged to discard.

## `tracestate` is the expensive header, and it grows at every hop

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ParseTraceState` (1 entry) | 191.5 | 48 | 2 |
| `ParseTraceState` (4 entries) | 578.9 | 192 | 2 |
| `StateValue.Insert` (into 3) | 141.7 | 128 | 1 |
| `StateValue.Get` (of 4) | 9.562 | 0 | **0** |
| `StateValue.String` (3 entries) | 265.4 | 120 | 4 |
| `Extract` with tracestate | 567.3 | 96 | 2 |

The actionable fact: **carrying a tracestate roughly triples the cost of
`Extract`** — 199.6 ns to 567.3 ns — and the list grows by one entry at every
hop, so the cost is a property of how deep the caller sits in a call graph, not
of this code. Four entries cost 3× one entry, which is linear as it should be.

`Get` at 9.56 ns and zero allocations is a linear walk, and that is correct for
a list this short: the specification caps `tracestate` at 32 entries, and a map
would cost more to build than the walk ever saves.

`String` is the only four-allocation call here. It is the outbound rendering,
paid once per outbound request, and it is left alone deliberately — a
`strings.Builder` with a presized buffer would trade four small allocations for
one larger one on a path that is already dominated by the network call it
precedes.

## Left alone, with the reason

`FormatTraceParent` (145.6 ns, 1 alloc) and `TraceID.String` (54.9 ns, 1 alloc)
both **return a string**, so the allocation is structural — it is the returned
value. Removing it means an `Append`-style API, which is a published-surface
change for a benefit that is invisible next to the outbound call it accompanies.
Recorded rather than done.

## Reproducibility envelope

> **Numbers vary across machines.** This run shared the box with four other
> jobs. The zero-allocation column is an invariant and does not vary; the
> nanoseconds do.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9b3c610 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/trace
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkParseTraceParent_Valid-8       	 9490064	       127.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseTraceParent_Malformed-8   	100000000	        10.83 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseTraceParent_AllZero-8     	17081222	        61.65 ns/op	       0 B/op	       0 allocs/op
BenchmarkFormatTraceParent-8            	 8318391	       145.6 ns/op	      64 B/op	       1 allocs/op
BenchmarkParseTraceID-8                 	22840500	        52.89 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseSpanID-8                  	39926600	        31.53 ns/op	       0 B/op	       0 allocs/op
BenchmarkTraceID_String-8               	21618632	        54.90 ns/op	      32 B/op	       1 allocs/op
BenchmarkParseTraceState_1-8            	 6459033	       191.5 ns/op	      48 B/op	       2 allocs/op
BenchmarkParseTraceState_4-8            	 2041093	       578.9 ns/op	     192 B/op	       2 allocs/op
BenchmarkStateValue_Insert-8            	 9078086	       141.7 ns/op	     128 B/op	       1 allocs/op
BenchmarkStateValue_Get-8               	125534322	         9.562 ns/op	       0 B/op	       0 allocs/op
BenchmarkStateValue_String-8            	 4895709	       265.4 ns/op	     120 B/op	       4 allocs/op
BenchmarkExtract-8                      	 6004743	       199.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkExtract_WithState-8            	 2273522	       567.3 ns/op	      96 B/op	       2 allocs/op
BenchmarkExtract_Absent-8               	47024042	        24.86 ns/op	       0 B/op	       0 allocs/op
BenchmarkInject-8                       	 6826572	       176.6 ns/op	      64 B/op	       1 allocs/op
BenchmarkInject_Invalid-8               	94023556	        12.15 ns/op	       0 B/op	       0 allocs/op
ok  	github.com/kitsunium/sdk/internal/core/trace	20.455s
```
