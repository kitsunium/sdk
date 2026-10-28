# ADR 0199 — a loaded io_uring loop groups its wait, and a native loop yields to a spare P

- **Status**: Accepted
- **Date**: 2026-10-28
- **Accepted**: 2026-10-28, on the measurements of a single-vCPU HTTP
  benchmark of the loop engine (go-loop-kodflow, its S9 profile and A/B runs)
  and of a C io_uring server of the same benchmark that groups its waits. It
  can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0196](0196-a-drain-stops-the-routing-first-takes-the-listen-queue-then-closes-and-loopsauto-reads-gomaxprocs-once.md)
  §D1 (`LoopsAuto` is GOMAXPROCS less one); [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md)
  is unchanged: `uring-min-timeout` stays class B, D3 says why
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D5 (`LoopValue`)

## Context

Profiled at 10,000 users on one vCPU, a loop goroutine locked to its thread
spent its CPU in two places the loop could change.

**The scheduler.** A locked loop never goes through the Go scheduler on its
own: it runs, waits in `io_uring_enter` or `epoll_wait`, runs again. Its P's
`schedtick` never moves, so sysmon took it for a goroutine that hogs its P,
preempted it every 10 ms, retook the P in the middle of a wait, handed it off
and went back to polling every 20 µs. Inclusive, `runtime.sysmon` was 10.9 %
of every sample. One `runtime.Gosched()` at most every 5 ms at a batch's end
took it to 1.2 % and the cost from 75.2 to 63.4 µs of CPU a request (2 ms cost
the same as 5 ms). The yield needs a P to hand over: the same build at
`GOMAXPROCS=1` cost 92.2 µs, and `GOMAXPROCS` = loops + 1 alone was worth
−6.4 µs. `LoopsAuto` gave every P a loop, leaving none.

**The wakeups.** Every batch waits for one completion. Under load the loop
wakes for one or two completions, serves them, and sleeps again. The C server
that leads the benchmark waits for 128 completions or 4 ms once its last batch
reaped four or more.

## Decision

### D1 — A loaded io_uring loop waits for more than one completion, bounded

Once a batch has reaped at least four completions, every ring together, the
next wait on the primary ring asks the kernel for `LoopValue.BatchWaitMin`
completions (32 by default, at most `CQBatch`) with `LoopValue.BatchWait` as
its timeout (250 µs by default, at most the 10 ms tick). It is one
`io_uring_enter` with `IORING_ENTER_EXT_ARG`, the timeout relative, as the
plain wait already is: the kernel wakes the thread once the CQ holds that many
entries past its head, or when the timeout runs out, and an `ETIME` with
completions in the CQ returns zero (`io_cqring_wait`, io_uring/io_uring.c,
6.8 to 7.0). Whatever woke it, the loop reaps every ring and serves the batch.
A batch that reaped fewer than four, a draining loop and `BatchWait < 0` wait
as before: one completion or the tick.

The bound is what one grouped wait may add, not a request's latency: a
request whose bytes arrive over several batches, or a send that completes in
a later one, may meet a grouped wait at each, so it gains a multiple of the
bound. The default stays well under the benchmark's p99 (0.8–1.0 ms); 32 completions is a batch the loop serves in
less than that at the measured cost. The C server's 4 ms bound would gather
more, and cost more latency than a default should. Nothing is probed for it:
`EXT_ARG` is a stage of the probe already, so an io_uring loop always has it,
and with `BatchWait` negative the wait is exactly the former one. epoll and
the portable backend ignore both fields.

### D2 — A native loop yields every 5 ms, while a P is spare; `LoopsAuto` leaves one

At the very end of a batch — the buffers returned, on io_uring — a native
loop calls `runtime.Gosched()` once 5 ms have passed since its last yield.
The yield is on when, at `Start`, `GOMAXPROCS` exceeds the group's loops, and
off otherwise: with every P running a loop, a yield only hands the loop's own
P around, which the `GOMAXPROCS=1` run measured as a loss.

`LoopsAuto` is now `GOMAXPROCS` less one, at least one. The P left over runs
the rest of the process — the lanes' workers, timers, the collector's
workers — and is what the yield hands the thread's time to. With the default
`GOMAXPROCS=1` a group on `LoopsAuto` runs one loop and does not yield, as
before; a consumer who sets `GOMAXPROCS=2` on one CPU gets one loop that
yields, the measured best. The SDK still never sets `GOMAXPROCS`. An explicit
`Loops` is unchanged, and the yield follows the same rule.

The interval is not a field: the measurement shows no difference between 2
and 5 ms, and the bound is what the sysmon preemption needs (10 ms). The
start line reports `batch_wait`, `batch_wait_min` and `yield`.

### D3 — `IORING_FEAT_MIN_TIMEOUT` is not used

6.12 added `min_wait_usec` to `io_uring_getevents_arg`: wait for
`min_complete` until it runs out, then return on any completion, or keep
waiting for one until the timeout. That would let a grouped wait keep the
tick as its timeout. Its post-window rule is not what every kernel that
advertises the feature does: mainline 6.12 to 6.18 keep `iowq->cq_tail` at
head + `min_complete` once the window passes empty, so a lone completion then
waits up to the timeout. Commit e15cb2200b93 ("io_uring: fix min_wait wakeups
for SQPOLL") set it to the window's start, and 29fe1bd01b99 ("io_uring/wait:
fix min_timeout behavior", 2026-06) to one past it; both are in the 6.12.y and
6.18.y stable branches today, and the feature bit does not say which a kernel
has. D1's bounded timeout behaves the same on all of them, so the capability
stays class B, reported `available` and never on.

## Consequences

- A loaded io_uring loop wakes once per up to 32 completions instead of once
  per one or two; each grouped wait adds at most 250 µs, and a request that
  spans several batches may meet one at each.
- A group on `LoopsAuto` runs one loop fewer on a machine of two Ps or more.
  On a box where the loops were the whole workload this lowers the loop
  count; the spare P was measured as the cheaper process at one vCPU, and
  `Loops` set explicitly keeps the former count.
- A native loop with a spare P goes through the scheduler every 5 ms: sysmon
  no longer retakes its P during its waits.

## Breaking changes

`LoopsAuto` resolves to one loop fewer when `GOMAXPROCS` is two or more.
`LoopValue` gains two optional fields whose zero is the new default.

## Alternatives considered

- **`min_wait_usec` with the tick as the timeout** — refused, D3.
- **The yield always on** — refused: measured as a loss with no spare P.
- **`LoopsAuto` unchanged, the reserve documented** — refused: the measured
  configuration is loops + 1, and a consumer reading `LoopsAuto` as "the
  right count" would never get it without knowing this ADR.
- **The grouped wait off by default** — refused: its bound is explicit and
  small, and the loop's behaviour unloaded is unchanged.

## Deferred

- The idle cost (13 ms of CPU a second at 22 req/s): sysmon still retakes the
  P during a wait longer than 10 ms, which the yield cannot prevent.
- A measure of D1 alone on the benchmark host, to tune its defaults.

## References

- `io_uring_enter(2)`, `IORING_ENTER_EXT_ARG`; liburing
  `io_uring_submit_and_wait_min_timeout(3)`.
- Linux io_uring/io_uring.c `io_cqring_wait`, `io_cqring_min_timer_wakeup`
  (v6.12, v6.18, linux-6.12.y, linux-6.18.y), io_uring/wait.c (v7.0).
- `internal/service/net/loop/uring_wait_linux_test.go` pins D1's kernel
  semantics on a COOP_TASKRUN and a DEFER_TASKRUN ring.
