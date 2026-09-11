<!-- generated from pkg/v1/id/id_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/id/` to refresh -->
# Benchmarks — `pkg/v1/id`

Seven identifier schemes (ADR 0024 / ADR 0038). A caller choosing between them
is choosing between properties — sortability, embedded time, length, entropy,
whether it names an entity — and cost was the one axis nobody had published.
This is that table.

## The choice table

| scheme | ns/op | B/op | allocs | sortable | carries a time | length |
|---|---:|---:|---:|:---:|:---:|---:|
| **NanoID** | **215.4** | 24 | 1 | no | no | 21 |
| UUIDv4 | 275.4 | 48 | 1 | no | no | 36 |
| Snowflake | 318.9 | 24 | 1 | yes | yes (ms) | ≤19 |
| UUIDv7 | 366.9 | 48 | 1 | yes | yes (ms) | 36 |
| ULID | 554.4 | 32 | 1 | yes | yes (ms) | 26 |
| TypeID | 641.7 | 32 | 1 | yes | yes (ms) | 26 + prefix |
| **KSUID** | **2 326** | 32 | 1 | yes | yes (s) | 27 |

Every scheme is **one allocation** — the returned string, which is structural.

The practical reading: **the six cheap schemes are all within 2.6× of each
other**, so the choice between them should be made on properties and not on
this column. KSUID is the exception at **10.8× the cheapest**, and that is worth
knowing before it goes on a per-request path.

## Why KSUID costs 10× — it is the format, not the implementation

A CPU profile puts **43.55 % flat in `base62Encode`**. Base62 is not a bit
slice: rendering a 160-bit value as 27 base62 characters means 27 rounds of
schoolbook long division over the whole 20-byte number. UUID, ULID and NanoID
all render by masking bits, which is why they are an order of magnitude apart.

That cost is inherent to the encoding KSUID chose and is **not** optimised here.
It is stated so a caller can decide: KSUID buys a second-resolution sortable id
in a URL-safe alphabet, and it costs about 2 µs.

## One optimisation the profile did find, on the read side

`ParseKSUID` was **836.9 ns**. The same profile showed `indexbytebody` at
**13.74 %**: `base62Decode` resolved each character with
`strings.IndexByte(base62Alphabet, c)` — a linear scan of up to 62 bytes, run 27
times per identifier.

Replacing it with a 256-entry reverse table makes the lookup a single load:

| | before | after |
|---|---:|---:|
| `ParseKSUID` | 836.9 ns · 0 allocs | **583.7 ns · 0 allocs** |

**1.43× faster, still zero allocations.** The table is *derived* from
`base62Alphabet` at package initialisation rather than written out, because a
hand-transcribed table that disagreed with the alphabet would decode some
identifiers to the wrong value and reject others as malformed, and nothing would
say so.

## The registry costs 7 ns, so config-driven code pays almost nothing

`New(UUIDv7Scheme)` is 373.9 ns against `UUIDv7()`'s 366.9 ns. Choosing a scheme
from configuration rather than from a call site costs about **2 %** — the
registry lookup is not a reason to hard-code a scheme.

## Snowflake under contention

`SnowflakeParallel` is 380.4 ns against 318.9 ns serial — **1.19×** on eight
goroutines. Snowflake is the one scheme with shared mutable state, a per-node
sequence counter that must not repeat within a millisecond, so it is the only
one whose contended cost differs at all. A 19 % penalty for the guarantee is
cheap; the number exists so nobody has to guess whether it is.

## The read side

`ParseKSUID` (583.7 ns, **0 allocs**) recovers the issue time and payload
without allocating. `ParseTypeID` (871.1 ns, 1 alloc) returns two strings, so
one allocation is the returned value.

Both are more expensive than generating a NanoID. An identifier that carries
data is only worth it if that data is read; if it never is, the scheme is paying
twice for nothing.

## Reproducibility envelope

> **Numbers vary across machines.** This run shared the box with four other
> jobs. The allocation column is an invariant and does not vary; the
> nanoseconds do, and the RATIOS between schemes are what this table asserts.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 2b55a33 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/id
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkUUIDv4-8              	 4835546	       275.4 ns/op	      48 B/op	       1 allocs/op
BenchmarkUUIDv7-8              	 3583798	       366.9 ns/op	      48 B/op	       1 allocs/op
BenchmarkULID-8                	 2181150	       554.4 ns/op	      32 B/op	       1 allocs/op
BenchmarkSnowflake-8           	 4735834	       318.9 ns/op	      24 B/op	       1 allocs/op
BenchmarkNanoID-8              	 5797474	       215.4 ns/op	      24 B/op	       1 allocs/op
BenchmarkKSUID-8               	  514498	      2326 ns/op	      32 B/op	       1 allocs/op
BenchmarkTypeID-8              	 1926306	       641.7 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewDispatch-8         	 3188738	       373.9 ns/op	      48 B/op	       1 allocs/op
BenchmarkSnowflakeParallel-8   	 3415212	       380.4 ns/op	      24 B/op	       1 allocs/op
BenchmarkParseKSUID-8          	 2002248	       583.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseTypeID-8         	 1453509	       871.1 ns/op	      48 B/op	       1 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/id	14.409s
```
