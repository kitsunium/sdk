<!-- generated from internal/core/observe/trace/trace_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./observe/trace/` to refresh -->
# Benchmarks — `internal/core/observe/trace`

The identifiers a trace is built out of, and the `tracestate` list as a VALUE.

The W3C headers themselves — `ParseTraceParent`, `FormatTraceParent`,
`ParseTraceState`, `Extract` and `Inject` — are the engine's since ADR 0160 §4,
and their measurements moved with them: the two allocations this file recorded
removing from the inbound path, the refusal paths and the cost of carrying a
`tracestate` are `internal/service/observe/trace/BENCH.md` §6 now. What stays is
what the parsers are built out of — the hex decode of an identifier and the list
they produce.

## The identifiers

`ParseTraceID` and `ParseSpanID` are the hex decode the engine's traceparent
parser spends its time in, isolated so a profile can tell "the parser is slow"
from "hex is slow". Both decode into a fixed array and allocate nothing.

## The `tracestate` value

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `StateValue.Insert` (into 3) | 141.7 | 128 | 1 |
| `StateValue.Get` (of 4) | 9.562 | 0 | **0** |
| `StateValue.String` (3 entries) | 265.4 | 120 | 4 |

(historical run, below; the allocation columns of today's run are identical.)

`Get` at 9.56 ns and zero allocations is a linear walk, and that is correct for
a list this short: the specification caps `tracestate` at 32 entries, and a map
would cost more to build than the walk ever saves.

`String` is the only four-allocation call here. It is the outbound rendering,
paid once per outbound request, and it is left alone deliberately — a
`strings.Builder` with a presized buffer would trade four small allocations for
one larger one on a path that is already dominated by the network call it
precedes.

The benchmarks seed their lists through a `StateBuilder`, the way the engine's
parser fills one; the parser's own cost is §6 of the engine's file.

## Left alone, with the reason

`TraceID.String` (54.9 ns, 1 alloc) **returns a string**, so the allocation is
structural — it is the returned value. Removing it means an `Append`-style API,
which is a published-surface change for a benefit that is invisible next to the
outbound call it accompanies. Recorded rather than done.

## Reproducibility envelope

> **Numbers vary across machines.** The zero-allocation column is an invariant
> and does not vary; the nanoseconds do.

Today's run:

| Dimension | Value |
|---|---|
| CPU                | Apple M1 Pro, 10 cores |
| RAM                | 16 GiB |
| OS                 | macOS 26.6.2 (darwin) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git commit         | 86ae82b2 + the change that moved the parsers (refactor/sdk-tree-reorg--w4-p4-observe) |
| Generated (UTC)    | 2026-10-03 |
| Bench wall-clock   | `-benchtime=1s`, single run, machine under heavy load (load average 26-37 on 10 cores: parallel builds) |

The historical run these tables quote: 8 cores, 15 GiB, Linux 6.12.101+deb13-amd64,
amd64, go1.27.1 linux/amd64, commit 33c0c3e, 2026-09-10, `-test.benchtime=1s`,
single run, machine shared with four other jobs.

## Results

Today's run:

```
goos: darwin
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/core/observe/trace
cpu: Apple M1 Pro
BenchmarkParseTraceID-10         	27552426	        45.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseSpanID-10          	52764549	        44.78 ns/op	       0 B/op	       0 allocs/op
BenchmarkTraceID_String-10       	16880050	       129.2 ns/op	      32 B/op	       1 allocs/op
BenchmarkStateValue_Insert-10    	 7199662	       174.5 ns/op	     128 B/op	       1 allocs/op
BenchmarkStateValue_Get-10       	175884027	         5.909 ns/op	       0 B/op	       0 allocs/op
BenchmarkStateValue_String-10    	13030694	       131.2 ns/op	     120 B/op	       4 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/core/observe/trace	10.027s
```

The historical run (linux/amd64, commit 33c0c3e), the rows of the benchmarks
still in this package:

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/observe/trace
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkParseTraceID-8                 	22840500	        52.89 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseSpanID-8                  	39926600	        31.53 ns/op	       0 B/op	       0 allocs/op
BenchmarkTraceID_String-8               	21618632	        54.90 ns/op	      32 B/op	       1 allocs/op
BenchmarkStateValue_Insert-8            	 9078086	       141.7 ns/op	     128 B/op	       1 allocs/op
BenchmarkStateValue_Get-8               	125534322	         9.562 ns/op	       0 B/op	       0 allocs/op
BenchmarkStateValue_String-8            	 4895709	       265.4 ns/op	     120 B/op	       4 allocs/op
```
