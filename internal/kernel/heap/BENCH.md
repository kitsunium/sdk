<!-- generated from internal/kernel/heap/heap_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./heap/` to refresh -->
# Benchmarks — `internal/kernel/heap`

A generic binary heap. This package exists because `container/heap` is not
generic, so the benchmark that matters is the one against `container/heap`
itself: the claim "the interface costs something" is measured here rather than
asserted in a doc comment.

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
| Git branch         | `agent-a46efa5095b1886a4` (worktree off `jaimerias-que-tu-te-connect`) |
| Git commit         | `af712a6` (pre-commit) |
| Generated (UTC)    | 2026-09-09 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |
| Population         | 4096 elements (`benchSize`), fixed PCG sequence |

## Results

```
BenchmarkFill-8                                 7968   133037 ns/op   128249 B/op   16 allocs/op
BenchmarkFillAndDrain-8                         1774   672848 ns/op   128248 B/op   16 allocs/op
BenchmarkPushPop_SteadyState-8              11943180       96.90 ns/op     0 B/op    0 allocs/op
BenchmarkBaseline_ContainerHeap_PushPop-8    6941188      169.8 ns/op     16 B/op    1 allocs/op
BenchmarkPeek-8                           1000000000        0.3676 ns/op   0 B/op    0 allocs/op
```

## The number that justifies the package

**`Push`+`Pop` at a population of 4096: 96.9 ns generic against 169.8 ns through
`container/heap` — 1.75× — and 0 allocations against 1 (16 B).**

Both numbers are the same algorithm on the same input; the difference is
entirely what the pre-generics interface forces:

- **the allocation** is the `int` boxed into `any` on the way into `Push`. It is
  16 B every time, forever, for a value that already fits in a register;
- **the rest** is twelve interface method calls per sift (one `Less` and one
  `Swap` per level of a 4096-element heap, none of which can be inlined) plus
  the type assertion on the way out of `Pop`.

None of that is a criticism of `container/heap`, which predates type parameters
by a decade. It is the measurement behind the package doc's claim that the
generic version "costs one comparison function at construction and nothing at
all at the call site".

## The batch shape

`BenchmarkFill` (133 µs) builds a 4096-element heap from empty;
`BenchmarkFillAndDrain` (673 µs) fills and then empties it. The difference —
**≈ 540 µs for 4096 pops, or ≈ 132 ns each** — is the cost of the sift-down,
which is higher than the steady-state `Push`+`Pop` because a drain shrinks the
heap through every depth rather than staying at one.

The 16 allocations per fill (128 KiB) are `append` doubling the backing array
from nothing to 4096 `int`s: 12 growth steps plus the tail. A heap that lives
across many fills pays this once, which is why `New` deliberately allocates
nothing (`TestNewLeavesTheArrayNil`).

## `Peek` is free

`BenchmarkPeek` at 0.37 ns/op is a bounds check and a slice index — below the
cost of the benchmark loop itself. It is in the table only so `Pop`'s 96.9 ns
can be read as "the sift-down", not "reading the top".

## What is deliberately not measured

- **Concurrency.** A `Heap` is not safe for concurrent use, on purpose: the
  caller picks the lock, or does not need one. Benchmarking a mutex around it
  would measure the caller's decision, not this package's.
- **A comparison function that is expensive.** The comparison dominates as soon
  as it costs more than a few nanoseconds, and at that point the numbers above
  say nothing useful — they would just report the caller's `cmp`.
