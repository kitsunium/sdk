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
