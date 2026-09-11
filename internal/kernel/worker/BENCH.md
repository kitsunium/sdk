<!-- generated from internal/kernel/worker/worker_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./worker/` to refresh -->
# Benchmarks — `internal/kernel/worker`

`LoopDaemon` is a background goroutine with an idempotent `Stop` and a join.
The package had no benchmarks, so the two questions a caller actually asks —
*what does a daemon cost to create* and *what does being stoppable cost my
loop* — had no answers. They do now.

## 1. A daemon costs ~1.3 µs and 4 allocations. It is a startup object.

`Start_Stop` is 1 306 ns / 296 B / 4 allocs for the full lifecycle: allocate the
daemon and its two channels, spawn a goroutine, close `stop`, and **join**.

`Start_StopAlreadyExited` — where the loop returns immediately, so `Stop`'s join
usually finds `done` already closed — is 1 296 ns, within noise of the first.
The hand-off is therefore **not** the cost; the goroutine spawn and the four
allocations are. Nothing in this package can shrink that.

The conclusion is the useful part: **1.3 µs is four hundred times a `Load` from
`snapshot` and five times a `Set` into `kernel/cache`.** A `LoopDaemon` is
something a process creates at startup and stops at shutdown. Creating one per
request would be a defect, and this is the number that makes it obvious rather
than arguable.

`Start_StopParallel` at 558.2 ns — **2.3× faster than serial**, with identical
allocations — confirms nothing serialises across daemons: each owns its own
channels and `sync.Once`, so eight cores create eight daemons in parallel and
the only shared resource is the allocator.

## 2. Being stoppable costs a loop 9.1 ns per iteration

`LoopIteration_StopCheck` is 9.113 ns / 0 allocs. That is one non-blocking
`select` on the stop channel, measured from inside a real daemon rather than
simulated.

The actionable form: at 9 ns, the check belongs in the **outer** loop of any
body doing real work, and it is affordable in an inner loop only if that inner
iteration is itself well above ~100 ns. A loop checking `stop` around a 5 ns
operation spends two thirds of its time asking whether to keep going.

## 3. Idempotent `Stop` is 19.71 ns — so `defer d.Stop()` is free

`Stop_Repeat` is 19.71 ns / 0 allocs: after the first call, `sync.Once` takes
its fast path and the join is a receive on a closed channel.

This matters because the defensive pattern a careful caller writes —
`defer d.Stop()` next to an explicit `Stop()` on the shutdown path — costs
20 ns, not a second goroutine join. The idempotence documented on `Stop` is
therefore usable and not merely safe.

`Done` is 2.836 ns / 0 allocs: a field read, pinned as one so the accessor is
never tempted into constructing anything.

## What is NOT measured here

`Every` has no benchmark, deliberately. Its cost is `time.Ticker`'s tick
delivery, which is the runtime's timer wheel rather than this package, and any
benchmark of it would measure the wall clock — spending real seconds to report
a number that belongs to `time`. The `LoopDaemon` half it is built on is
measured above, and that is the half this package owns.

## Reproducibility envelope

> **Numbers vary across machines.** Goroutine spawn and scheduler round trips
> are the most machine- and load-sensitive numbers in this repository; this run
> shared the box with four other jobs. Read the ratios — daemon-vs-operation,
> serial-vs-parallel — rather than the absolute microseconds.

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
pkg: github.com/kitsunium/sdk/internal/kernel/worker
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkStart_Stop-8                	  963840	      1306 ns/op	     296 B/op	       4 allocs/op
BenchmarkStart_StopAlreadyExited-8   	  847222	      1296 ns/op	     296 B/op	       4 allocs/op
BenchmarkStop_Repeat-8               	59867763	        19.71 ns/op	       0 B/op	       0 allocs/op
BenchmarkDone-8                      	427456238	         2.836 ns/op	       0 B/op	       0 allocs/op
BenchmarkLoopIteration_StopCheck-8   	131866000	         9.113 ns/op	       0 B/op	       0 allocs/op
BenchmarkStart_StopParallel-8        	 2199564	       558.2 ns/op	     296 B/op	       4 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/kernel/worker	8.664s
```
