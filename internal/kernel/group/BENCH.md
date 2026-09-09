<!-- generated from internal/kernel/group/group_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./group/` to refresh -->
# Benchmarks — `internal/kernel/group`

Structured concurrency. These benchmarks answer one question and it is not "how
fast is it": **when is a `Group` worth using at all?** The primitive buys a wait
point, a first error, a bound, and a panic that does not kill the process — and
it pays for all four in goroutines and scheduler latency, so the only meaningful
comparison is against calling the tasks directly.

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

## Results

```
BenchmarkGoWait_OneTask-8                   487167     2087 ns/op     312 B/op    5 allocs/op
BenchmarkGoWait_EightTasks_Unlimited-8      142622     7813 ns/op     480 B/op   12 allocs/op
BenchmarkGoWait_EightTasks_Limit2-8         111837    10410 ns/op     480 B/op   12 allocs/op
BenchmarkGoWait_EightTasks_Serial-8         129490     8661 ns/op     480 B/op   12 allocs/op
BenchmarkCollect_EightTasks-8               149168     9367 ns/op    1056 B/op   21 allocs/op
BenchmarkBaseline_EightDirectCalls-8     177795363        6.538 ns/op   0 B/op    0 allocs/op
```

## The number that decides whether to use this

**A one-task group costs ≈ 2.1 µs.** That is `BenchmarkGoWait_OneTask`: one
submission, one wait, a task that returns immediately. Only 312 B and five
allocations of it are memory (the group, the cancel-cause context, the semaphore
channel, the goroutine's initial frame); the rest is a **goroutine
park/unpark round trip** — the caller hands off, blocks on a `WaitGroup`, and is
woken by the scheduler.

So the rule, measured rather than asserted:

> A `Group` pays for itself when the tasks cost materially more than ~2 µs each
> **and** there is more than one of them.

A network round trip, a disk read, a key derivation, a subprocess — all far
above the line. Eight arithmetic results — `BenchmarkBaseline_EightDirectCalls`,
6.5 ns for all eight — is three orders of magnitude below it, and wrapping that
in a group buys a wait point for work that has already finished.

The baseline is in the table for exactly this reason: without it, 2 µs reads as
"fast".

## Eight tasks, three limits

The three eight-task rows share one shape and differ only in the bound, so the
gaps between them are the price of the bound itself:

| Limit | ns/op | vs unlimited |
|---|---|---|
| `Unlimited` | 7813 | — |
| `0` → clamped to 1 (serial) | 8661 | +11 % |
| `2` | 10410 | +33 % |

Two things are worth stating out loud.

- **The clamp is cheap.** A caller who forgot the limit and got serial execution
  pays 11 % here, not a deadlock — which is the whole point of ADR 0031 applied
  to this primitive. On a real workload the ratio is different (serial execution
  of eight 10 ms tasks costs 80 ms against 10 ms), but the *primitive* is not
  what makes it expensive.
- **A binding limit costs more than no limit.** `Limit2` is the slowest row
  because the submitting goroutine actually parks on the semaphore between
  submissions, so the fan-out is paid for in two extra park/unpark cycles rather
  than in memory. A bound is not free; it is bought.

Allocations are identical across all three (480 B, 12 allocs) because the
semaphore is a `chan struct{}` — a zero-size element type, so its buffer costs
nothing at any capacity. That is also what lets `Unlimited` be `math.MaxInt`
rather than a sentinel the code has to branch on, and
`TestUnlimitedSemaphoreIsAllocatedNotApproximated` is the guard.

## What `Collect` adds

`BenchmarkCollect_EightTasks` (9367 ns, 1056 B, 21 allocs) against
`BenchmarkGoWait_EightTasks_Unlimited` (7813 ns, 480 B, 12 allocs): **+20 % time
and +576 B** for the result slice and the eight closures that write into it.
That is the price of the typed fan-out, and it is small enough that the choice
between `Collect` and a hand-written `Group` should be made on readability
rather than on this table.

## What is deliberately not measured

- **The panic path.** Capturing a stack with `runtime/debug.Stack` is expensive
  by design, and benchmarking it would invite someone to make it cheaper by
  dropping the stack — which is the one thing that makes the captured panic
  worth having. It is a correctness property, pinned by
  `TestPanicInATaskReachesTheWaiterCarryingTheFailingStack`.
- **Contended `Go` from many submitters.** The semaphore is a channel and
  behaves like one; there is no group-specific contention to discover.
