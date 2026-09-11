<!-- generated from internal/kernel/recycler/recycler_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./recycler/` to refresh -->
# Benchmarks — `internal/kernel/recycler`

`Pool[T]` and `CappedPool[T]` exist for exactly one reason — to be faster than
allocating — and until this file they had never been measured. Two results
below change how the package should be used.

## 1. Pool a POINTER. A bare slice costs 24 B on every Put

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Pool_GetPut_Buffer4K` — pool holds `[]byte` | 51.36 | 24 | 1 |
| `Pool_GetPut_BufferPtr4K` — pool holds `*[]byte` | **25.70** | **0** | **0** |
| `Pool_Parallel` — `[]byte` | 19.82 | 24 | 1 |
| `Pool_ParallelPtr` — `*[]byte` | **3.496** | **0** | **0** |

`sync.Pool` stores `any`. A pointer fits in an interface word and is boxed for
free; a slice header is three words and cannot be, so `Put([]byte)`
heap-allocates 24 B to carry it — **every call**. Pooling a `*[]byte` instead
is 2× faster serially and **5.7× faster in parallel**, at zero allocations.

Note that `Pool_ParallelPtr` at 3.496 ns is faster than the same operation on a
single goroutine. That is `sync.Pool`'s per-P shards working exactly as
designed: with no boxing allocation to serialise on the allocator, each P
services its own free list and the cores stop talking to each other.

The package doc claimed callers recycle "without paying the boxing cost". That
is true only for pointer-shaped `T`, and the comment now says so. **Every
consumer in this repository already pools a pointer** — `kernel/buffer` holds
`*[]byte`, `core/codec/scratch` holds `*bytes.Buffer` and `*bytes.Reader`,
`service/logger` holds `*chainBuilder`, `middleware/async` holds `*recordEntry`,
`net/server` holds `*pooledConn` — so nothing in the tree pays this. The
benchmark pair exists to keep the next consumer honest.

## 2. The pool's cost is FLAT; the allocation it replaces is not

| payload | pooled Get+Put | `make` | ratio |
|---|---:|---:|---:|
| 4 KiB | 51.36 ns | 1 554 ns | **30×** |
| 64 KiB | 54.04 ns | 14 845 ns | **275×** |

A 16× larger buffer costs the pool **5 % more** and costs the allocator 9.6×
more. The pool's price is a per-P lookup and does not depend on what it
recycles, which is the whole argument for the primitive — and the reason its
benefit grows with the size of what you hand it.

## 3. For a small object, pooling wins — but barely, and it is not free

`Pool_GetPut_Small` is 20.29 ns / 0 allocs against `New_Small` at 24.64 ns /
1 alloc / 32 B. A four-word struct behind a pointer is already cheap to
allocate, so the pool buys ~4 ns and one GC-pressure unit. Worth it on a path
that runs millions of times; noise anywhere else. **Do not pool something small
because pooling is available** — pool it because you measured the allocation.

## 4. `CappedPool` misconfigured is an allocator wearing a pool's name

`CappedPool_GetPut_UnderCap` is 64.46 ns; `CappedPool_GetPut_OverCap` is
**1 783 ns with 2 allocs**, a 28× regression. The over-cap benchmark sets
`maxCap` *below* the factory's own capacity, so every `Put` orphans the value
and every `Get` misses and rebuilds it. Nothing fails, nothing warns, and the
type still says `CappedPool`.

That configuration is legal by construction — `NewCappedPool` only refuses a
non-positive `maxCap`, because a threshold below the typical size is a
legitimate choice for a pool that must not retain outliers. This benchmark is
what it costs when it is a mistake instead.

The ~13 ns between `UnderCap` and the plain `Pool_GetPut_Buffer4K` is the cap
check plus the reset call: what the discard-before-reset policy costs on the
happy path.

## Reproducibility envelope

> **Numbers vary across machines** — and this run was taken on a shared box with
> four other jobs active, so absolute `ns/op` carry real run-to-run variance
> (`Buffer4K` measured 51 ns here and 83 ns on a busier pass). **The ratios are
> what this report asserts**: pointer-vs-slice, pooled-vs-`make`, flat-vs-linear.
> Those held across every run.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9adba64 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/recycler
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkPool_GetPut_Small-8            	59718727	        20.29 ns/op	       0 B/op	       0 allocs/op
BenchmarkNew_Small-8                    	44788792	        24.64 ns/op	      32 B/op	       1 allocs/op
BenchmarkPool_GetPut_Buffer4K-8         	24304731	        51.36 ns/op	      24 B/op	       1 allocs/op
BenchmarkNew_Buffer4K-8                 	  824965	      1554 ns/op	    4096 B/op	       1 allocs/op
BenchmarkPool_GetPut_Buffer64K-8        	19177189	        54.04 ns/op	      24 B/op	       1 allocs/op
BenchmarkNew_Buffer64K-8                	   87289	     14845 ns/op	   65536 B/op	       1 allocs/op
BenchmarkPool_Parallel-8                	80598091	        19.82 ns/op	      24 B/op	       1 allocs/op
BenchmarkPool_GetPut_BufferPtr4K-8      	45264038	        25.70 ns/op	       0 B/op	       0 allocs/op
BenchmarkPool_ParallelPtr-8             	390679272	         3.496 ns/op	       0 B/op	       0 allocs/op
BenchmarkCappedPool_GetPut_UnderCap-8   	17068155	        64.46 ns/op	      24 B/op	       1 allocs/op
BenchmarkCappedPool_GetPut_OverCap-8    	  724784	      1783 ns/op	    4121 B/op	       2 allocs/op
```
