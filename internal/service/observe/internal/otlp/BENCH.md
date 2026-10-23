<!-- generated from internal/service/observe/internal/otlp/marshal_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -count=5 ./observe/internal/otlp/` to refresh -->
# Benchmarks — `internal/service/observe/internal/otlp`

One question, asked when the two signals' OTLP code was merged here: **should
the OTLP/JSON marshal borrow a pooled buffer instead of a fresh `bytes.Buffer`
per export?** It is the obvious optimisation, the SDK has the primitives for it
(`internal/kernel/concur/buffer`, `internal/core/data/codec/scratch`), and it was measured
rather than assumed. The answer is **no**, and the pooled variant is kept in
the suite as a CONTROL (`_PooledControl`), the way `internal/service/observe/trace`
keeps its `RWMutex` control, so the answer can be re-asked by running one
command.

## The appenders — what the signals pay now

The "next win" named at the end of the headline below was taken, without
`encoding/json/v2` (still behind `GOEXPERIMENT=jsonv2` in Go 1.27.3): `append.go`
writes the document into ONE buffer — `AppendString` with encoding/json's own
escaping, `AppendKey`, and an `AppendJSON` on each scalar and shared message —
and each signal renders its tree with them. `Marshal` stays, as the reference
the appenders are tested against byte for byte (`append_external_test.go`, and
each signal's `otlp_append_internal_test.go` on 2 000 random trees).
`BenchmarkAppend_*` renders the very trees `BenchmarkMarshal_*` marshals —
`TestBenchAppendIsMarshal` keeps the two documents identical — so the rows
below price the same bytes:

| one binary, `-count=10` | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Marshal`, 10 points | 34.3 µs ± 1 % | 4 483 | 45 |
| appenders, 10 points | **4.6 µs ± 1 %** | 4 864 | **1** |
| `Marshal`, 1 000 points | 3.21 ms ± 1 % | 416 KB | 4 005 |
| appenders, 1 000 points | **0.44 ms ± 2 %** | 451 KB | **1** |
| build the tree + `Marshal`, 1 000 points | 3.79 ms ± 2 % | 939 KB | 9 015 |
| build the tree + appenders, 1 000 points | **0.91 ms ± 1 %** | 692 KB | **5 004** |

−86 % on the marshal and 4 004 allocations fewer per 1 000 points: the
document is the one allocation left, sized from an estimate (a little over in
B/op, never a second buffer); the 5 000 that remain in an export are the tree's.
In the signals, A/B against the base binary, interleaved, `-count=10`, every
delta p=0.000: `metrics` 1 000 counter points −78 % time / −44 % allocations,
1 000 histogram points −74 % / −84 %; `trace` 512 spans −73 % / −33 % (metrics
BENCH.md §OTLP/JSON encoding, trace BENCH.md §8).

`Marshal`'s own rows read +4 % in the interleaved A/B against the base binary,
on unchanged code. It is the test binary's layout: rebuilt without
`append_external_test.go` (the library change kept), the same A/B reads
−0.1 % (p=0.88 to 1.00). No signal calls `Marshal` any more.

Measured on: AMD EPYC 7351P 16-Core, 8 vCPU visible, 11 GiB RAM, Linux
6.12.101+deb13-amd64 (Debian 13), go1.27.3 linux/amd64, base d91642be, branch
`perf/otlp-json`, 2026-10-23, benchtime 1s.

## The headline: the buffer is one allocation in nine thousand

| | ns/op (5 runs) | B/op | allocs/op |
|---|---|---:|---:|
| `Marshal`, 10 points | 16.5 – 28.2 µs | 4 483 | **45** |
| control: pooled buffer + encoder, 10 points | 13.4 – 15.7 µs | 4 327 | **43** |
| `Marshal`, 1 000 points | 1.71 – 3.90 ms | 410 – 412 KB | **4 005** |
| control: pooled buffer + encoder, 1 000 points | 2.07 – 2.91 ms | 431 – 447 KB | **4 004** |
| build the tree + `Marshal`, 1 000 points | 2.66 – 4.30 ms | 842 – 891 KB | **9 013** |

Why the pool does not pay:

- **`encoding/json` already pools the buffer that matters.** `Encoder.Encode`
  renders into an `encodeState` taken from its own `sync.Pool` and writes the
  finished bytes to the destination in ONE `Write`. A fresh `bytes.Buffer`
  therefore costs exactly one allocation of the document's exact size — and
  that allocation IS the returned document. What a pool can save is the
  `Buffer` and `Encoder` headers: **two small allocations per export**, which is
  the 45 → 43 above.
- **The document is returned, so a pooled buffer must be copied out.** At
  1 000 points the copy plus the pooled buffer's retained growth makes the
  control **5–9 % heavier in bytes**, for one allocation fewer out of 4 005.
- **The cost is elsewhere.** Building and marshalling 1 000 points is 9 013
  allocations: ~5 000 build the tree (a `KeyValue` slice per point, a pointer
  per oneof value) and ~4 000 are the proto3-JSON scalars' own `MarshalJSON` —
  two timestamps and a value per point, each a fresh `[]byte` that
  `encoding/json` then re-validates. The buffer is one of the 9 013.

The nanoseconds swing up to 2× between consecutive runs of the same binary on
this box (it was shared with concurrent builds), so the allocation and byte
columns are the evidence; the time column is reported for completeness and
decides nothing.

**Where the next win is, if one is wanted**: the scalars. An `Int64` /
`Uint64` / `Double` that appended into the encoder's own state instead of
returning a fresh slice would remove up to three allocations per point — but
that is `encoding/json/v2`'s `MarshalJSONTo`, a change of encoder whose output
details (omission, escaping) must be re-proved against both signals'
byte-for-byte schema tests first. Named here, not done.

## Reproducibility envelope

> **Numbers vary across machines and under load.** The allocation columns are
> properties of the code and do not.

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro, 10 cores |
| RAM | 16 GiB |
| OS | macOS 26.6.2 (darwin) |
| Architecture | arm64 |
| Go toolchain | go1.27.1 darwin/arm64 |
| Git branch | refactor/sdk-tree-reorg--p2a-observe |
| Git commit | f0a1e016 (the tree measured; the benchmark itself lands in the next commit) |
| Generated (UTC) | 2026-10-03 |
| Bench wall-clock | `-benchtime=1s -count=5`, machine under load |

## Results

```
goos: darwin
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/service/observe/internal/otlp
cpu: Apple M1 Pro
BenchmarkMarshal_10-10                    	   86379	     19580 ns/op	    4484 B/op	      45 allocs/op
BenchmarkMarshal_10-10                    	   58258	     23789 ns/op	    4483 B/op	      45 allocs/op
BenchmarkMarshal_10-10                    	   66955	     16890 ns/op	    4483 B/op	      45 allocs/op
BenchmarkMarshal_10-10                    	   83320	     16529 ns/op	    4483 B/op	      45 allocs/op
BenchmarkMarshal_10-10                    	   54620	     28195 ns/op	    4483 B/op	      45 allocs/op
BenchmarkMarshal_1000-10                  	     373	   3904534 ns/op	  412055 B/op	    4005 allocs/op
BenchmarkMarshal_1000-10                  	     604	   2420501 ns/op	  412423 B/op	    4005 allocs/op
BenchmarkMarshal_1000-10                  	     697	   2287550 ns/op	  410724 B/op	    4005 allocs/op
BenchmarkMarshal_1000-10                  	     666	   1709084 ns/op	  411244 B/op	    4005 allocs/op
BenchmarkMarshal_1000-10                  	     571	   1972912 ns/op	  410463 B/op	    4005 allocs/op
BenchmarkMarshal_10_PooledControl-10      	   96984	     13796 ns/op	    4327 B/op	      43 allocs/op
BenchmarkMarshal_10_PooledControl-10      	   91612	     15682 ns/op	    4326 B/op	      43 allocs/op
BenchmarkMarshal_10_PooledControl-10      	   90804	     14136 ns/op	    4327 B/op	      43 allocs/op
BenchmarkMarshal_10_PooledControl-10      	   78502	     15450 ns/op	    4327 B/op	      43 allocs/op
BenchmarkMarshal_10_PooledControl-10      	   85340	     13359 ns/op	    4327 B/op	      43 allocs/op
BenchmarkMarshal_1000_PooledControl-10    	     858	   2065879 ns/op	  446852 B/op	    4005 allocs/op
BenchmarkMarshal_1000_PooledControl-10    	     558	   2120753 ns/op	  443992 B/op	    4004 allocs/op
BenchmarkMarshal_1000_PooledControl-10    	     403	   2906375 ns/op	  431123 B/op	    4004 allocs/op
BenchmarkMarshal_1000_PooledControl-10    	     403	   2737645 ns/op	  434943 B/op	    4004 allocs/op
BenchmarkMarshal_1000_PooledControl-10    	     559	   2287808 ns/op	  442212 B/op	    4004 allocs/op
BenchmarkBuildAndMarshal_1000-10          	     242	   4296740 ns/op	  889315 B/op	    9014 allocs/op
BenchmarkBuildAndMarshal_1000-10          	     327	   3403403 ns/op	  890713 B/op	    9014 allocs/op
BenchmarkBuildAndMarshal_1000-10          	     375	   3218280 ns/op	  842332 B/op	    9013 allocs/op
BenchmarkBuildAndMarshal_1000-10          	     434	   2723116 ns/op	  846905 B/op	    9013 allocs/op
BenchmarkBuildAndMarshal_1000-10          	     716	   2660213 ns/op	  851877 B/op	    9013 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/service/observe/internal/otlp	33.751s
```
