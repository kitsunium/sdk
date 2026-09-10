<!-- generated from internal/core/metrics/attr_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./metrics/` to refresh -->
# Benchmarks — `internal/core/metrics`

ADR 0044 makes two claims that live in this package rather than in the meter
above it: that an attribute's **kind** is part of a series' identity, and that
the attributed hot path allocates **nothing**. `internal/service/metrics` had
benchmarks; the layer that makes the claims did not. These are them.

## The headline: every hot-path function allocates zero

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `String` | 10.34 | 0 | **0** |
| `Bool` | 10.22 | 0 | **0** |
| `Int64` | 3.513 | 0 | **0** |
| `Float64` | 2.901 | 0 | **0** |
| `AppendIdentity` (string) | 9.968 | 0 | **0** |
| `AppendIdentity` (int64) | 5.662 | 0 | **0** |
| `AppendIdentity` (4-attribute set) | 31.45 | 0 | **0** |
| `AppendText` | 6.190 | 0 | **0** |
| `CompareAttrValue` (same kind) | 7.126 | 0 | **0** |
| `CompareAttrValue` (different kind) | 5.197 | 0 | **0** |
| `ValidateAttrs` (4-attribute set) | 8.820 | 0 | **0** |
| `SortAttrs` (4-attribute set) | 247.2 | 192 | 1 |

`SortAttrs` is the only allocation, and its own doc comment already says why:
it is the **cold** form, copying and owning the caller's slice, called once at
construction by `Resource` and `Scope` and never per observation. The meter
uses a stack buffer instead. This number is the argument for that split, not a
regression.

`ValidateAttrs` at 8.82 ns for a four-attribute set is the number that makes
"checked on every instrument fetch" affordable — an empty key, an unset kind
and a duplicate key are all caught for less than the cost of one attribute
constructor.

## An identity costs about what the attributes cost

A realistic four-attribute observation builds its series identity in 31.45 ns
with zero allocations, reusing the caller's buffer. Compare the four
constructors that produced those attributes — roughly 27 ns together — and the
picture is that **encoding the identity is not the expensive part; nothing
here is.** For scale, `kernel/cache`'s `Fetch` hit is 59.89 ns and a contended
one 375.5 ns, so a full attributed observation costs less than half of one
uncontended cache lookup.

`AppendText` (6.19 ns) is the exporter-side, human-readable rendering and is
**not** the identity encoding. They are benchmarked separately so a profile
never confuses the two: `AppendIdentity` must be injective, `AppendText` must
be readable, and only the first one being wrong silently merges two series.

## One measured oddity, four causes excluded, deliberately not chased

`String` and `Bool` cost ~10.2 ns; `Int64` and `Float64` cost ~3.2 ns. A 3×
split between four constructors of the same 48-byte struct is odd enough to
check, so it was checked. Four hypotheses were tested and **all four refuted**:

| hypothesis | test | result |
|---|---|---|
| GC write barrier on the string field | store into a local sink instead of a package var | 10.14 vs 10.30 — no effect |
| constant folding favouring the numeric pair | non-constant args read from a slice | 10.63 / 10.49 / 4.07 / 3.93 — same split |
| benchmark ordering / code alignment | reversed declaration order, `-count=2` | same split, reproducible |
| an inlining-budget cliff (the ADR 0044 precedent) | `go build -gcflags=-m` | all four report `can inline` |

The split is therefore real, reproducible, and unexplained by the obvious
causes. It is **not** being chased, and that is a decision rather than an
omission: seven nanoseconds on an attribute constructor sits against a 59 ns
cache hit, a 375 ns contended one and a 3 ms atomic file publish elsewhere in
this SDK. Going further means reading assembly, which buys nothing measurable
in any caller.

The four refuted hypotheses are listed so the next person does not spend the
same hour rediscovering them. The ADR 0044 precedent — where `NewMeter` falling
under the inline budget silently cost 48 bytes per attributed observation — is
exactly why the inlining check was worth running even though it came back
negative.

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
| Git commit         | 39c0b37 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/metrics
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkString-8                           	100000000	        10.34 ns/op	       0 B/op	       0 allocs/op
BenchmarkBool-8                             	100000000	        10.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkInt64-8                            	351340664	         3.513 ns/op	       0 B/op	       0 allocs/op
BenchmarkFloat64-8                          	412767462	         2.901 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendIdentity_String-8            	120349398	         9.968 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendIdentity_Int64-8             	210481112	         5.662 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendIdentity_Set4-8              	38357604	        31.45 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendText-8                       	195398492	         6.190 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompareAttrValue_SameKind-8        	169512540	         7.126 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompareAttrValue_DifferentKind-8   	232973878	         5.197 ns/op	       0 B/op	       0 allocs/op
BenchmarkValidateAttrs_Set4-8               	136084549	         8.820 ns/op	       0 B/op	       0 allocs/op
BenchmarkSortAttrs_Set4-8                   	 5039287	       247.2 ns/op	     192 B/op	       1 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/core/metrics	14.178s
```
