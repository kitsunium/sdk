# ADR 0202 — a loop batch pays its fixed cost once, and a grouped wait starts on the request rate

- **Status**: Accepted
- **Date**: 2026-10-31
- **Accepted**: 2026-10-31, on the profile and the A/B runs of a sparse
  keep-alive load (`BenchmarkLoadSparse`) and the cost benchmarks of the
  loop's batch. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0199](0199-a-loaded-io-uring-loop-groups-its-wait-and-a-native-loop-yields-to-a-spare-p.md)
  §D1 (when the grouped wait starts); §D3 stands, on a new measure
- **Related**: [ADR 0200](0200-an-io-uring-close-is-a-shutdown-alone-and-the-accept-watchdog-judges-a-listener.md)
  (the accept watchdog this ADR paces)

## Context

A single-vCPU HTTP benchmark of the loop engine — ten thousand keep-alive
users, about a thousand requests a second — spends 59 µs of CPU a request
where a C io_uring server spends 53. Its profile puts 7.1 µs of that in the
SDK's loop, http1 and token code, against 2.3 µs for a hand-written epoll
server's whole path, and about 6 µs more in softirq and wakeups. At that
load a batch reaps 1.25 to 1.4 completions: what a batch costs whatever it
serves is paid about 1.6 times a request.

`BenchmarkLoadSparse` reproduces the load: a child serving the http1 chain
on one loop at `GOMAXPROCS=2`, 10,000 connections, 1,000 requests a second
each to a connection chosen at random, the child's CPU, context switches,
batches and its loop thread's wakeups reported a request. Profiled there,
three costs of a batch did not depend on its work:

- **The accept watchdog** called `getsockopt(TCP_INFO)` on every listener at
  every batch once the ring's last accept was older than its window — always,
  once every connection is open: about 5 % of the loop's samples.
- **The clock** was read five times a batch: `time.Now` at its start and end,
  each a wall and a monotonic read, and `time.Since` for the tick.
- **A wait with a completion already in the CQ** entered the kernel with
  `GETEVENTS` and nothing to submit.

And ADR 0199's grouped wait never started: it waits for a batch of four, and
a batch reaps one or two. Raised to 4 ms, the bound gathered enough for the
count to start it, but a window that gathered fewer than four dropped it.

## Decision

### D1 — A batch reads the clock once at each end

A batch reads the monotonic clock since the loop's epoch once at its start
(`begin`: the tick derived from it) and once at its end (`endBatch`: the
batch-duration histogram and the yield's interval). Nothing between reads it:
`SetDeadline` counts from the batch's tick, so a deadline set in a batch
longer than a tick fires that much earlier than asked, within the 10 ms the
wheel rounds to anyway.

### D2 — The accept watchdog looks once a tick

The watchdog judges a ring's listeners at most once a tick (10 ms): its
window is `watchdogTicks`, so a look per batch bought nothing a look per tick
does not.

### D3 — A wait with a completion ready makes no wait

When the primary's CQ already holds a completion, its wait submits what is
pending without waiting — `RawSyscall6`, no scheduler round trip — and makes
no system call when nothing is pending. With `DEFER_TASKRUN` the task work
that a `GETEVENTS` would have run is run by the next wait instead.

### D4 — The grouped wait starts on the request rate too

The loop keeps a moving average of the time between two arrivals — recv
completions carrying bytes, the time since the last batch that had some
shared among them, each batch weighing an eighth. The next wait is grouped
when the last batch reaped completions and either reaped four or more, as
before, or two arrivals at least are expected within `BatchWait`. Below two
an arrival a bound, a grouped window is as likely to hold none as one and
wakes the loop as often as the plain wait: it stays plain. A batch with no
arrival — sends completing, the tick — leaves the average as it was.

A grouped window whose bound passes with an empty CQ is waited through
plainly, with no batch served for it: the secondary rings and `Post` signal
through the primary's CQ, so an empty one is an idle loop.

`BatchWait`'s default stays 250 µs: at a thousand requests a second the rate
does not start it, and the load behaves as before. The bound is the
consumer's lever: a submission that accepts a few milliseconds a request sets
it, and the rate keeps the wait grouped where the count dropped it.

### D5 — `uring-min-timeout` stays class B, measured

`min_wait_usec` (ADR 0199 §D3) was measured on a 7.0 kernel, whose
post-window rule is the fixed one, as the grouped wait's form — the window at
`BatchWait`, the tick as the timeout — against the bounded wait, both
started by the same rule: the same batches, wakeups and CPU a request at
250 µs and at 1 ms. Used on every wait at 250 µs it added 36 % of context
switches: the kernel wakes the waiting task when a window ends empty, as the
bounded wait's timeout does. Nothing it buys pays for the self-test a
kernel's advertisement would need (the feature bit does not say which
post-window rule a kernel has), so the row stays as ADR 0199 left it.

## Measures

`BenchmarkLoadSparse`, `BenchmarkCore_batchClock`, `BenchmarkURingWait_ready`
and `BenchmarkURingWatchdog_quiet`, run in Docker (linux/arm64, kernel
7.0.14, one CPU for the child) on a shared host; main against this ADR's
code, interleaved run by run, benchstat medians.

| Benchmark | main | this ADR | delta |
|---|---|---|---|
| `Core_batchClock` (one empty batch's clock) | 138.4 ns | 70.6 ns | −49 % (p=0.000, n=10) |
| `Core_batch/open=10000` | 193.7 ns | 117.5 ns | −39 % (p=0.000, n=10) |
| `URingWait_ready` | 210.2 ns | 3.1 ns | −98.5 % (p=0.000, n=10) |
| `URingWatchdog_quiet` (16 batches a tick) | 215.2 ns | 16.2 ns | −92.5 % (p=0.000, n=10) |
| `LoadSparse/wait=4ms` batches a request | 0.304 | 0.210 | −31 % (p=0.008, n=5) |
| `LoadSparse/wait=4ms` loop wakeups a request | 0.460 | 0.424 | −7.9 % (p=0.008, n=5) |
| `LoadSparse/wait=4ms` context switches a request | 0.805 | 0.752 | −6.7 % (p=0.008, n=5) |
| `LoadSparse/wait=1ms` batches a request | 1.53 | 1.47 | ~ (p=0.222, n=5) |
| `LoadSparse/wait=1ms` loop wakeups a request | 0.949 | 0.909 | ~ (p=1.000, n=5) |

The CPU a request of `LoadSparse` stayed inside the host's spread (±10 to
±40 % between runs, other workloads sharing the host): the cost benchmarks
are the per-batch gains, the batch and wakeup counts the grouped wait's.
At 1 ms the rate does not start the wait (an arrival a millisecond), and the
counts are main's, where the count of D4's first version — a gap between
batches rather than arrivals — had added 5.6 % of wakeups.

## Consequences

- A batch costs about 70 ns less in clock reads, and a loop with every
  connection accepted no longer makes a system call a batch for its
  watchdog.
- With `BatchWait` of a few milliseconds a sparse load keeps its waits
  grouped: a third fewer batches, 8 % fewer wakeups of the loop's thread.
- The default load is unchanged: the rate starts the grouped wait only where
  two requests come within 250 µs.

## Alternatives considered

- **The grouped wait on the gap between batches.** Measured first: a batch
  of sends follows each batch of requests, so the gap halves the rate it
  reads, and a grouped window measures itself — at 1 ms it started the wait
  where one request a window came, and added wakeups. Refused for the
  arrivals' spacing.
- **`min_wait_usec` behind a behavioural self-test.** Refused, D5.
- **Reaping the sends' completions in the batch that issued them.** A send on
  a socket with room completes in the next `io_uring_enter`, so a request
  costs a second, near-empty batch: 0.6 batches a request at this load.
  Reaping them in the issuing batch would reap any completion behind them —
  a recv after `OnBatchEnd`, which the batch's contract forbids. Deferred.

## Deferred

- The yield's own cost (ADR 0199 §D2): under this load `runtime.Gosched`
  and the M it wakes are 3 to 4 % of the loop's samples.
- The near-empty batch of the sends' completions (above).

## References

- `internal/service/net/server/load_sparse_linux_test.go`
- `internal/service/net/loop/uring_cost_linux_test.go`,
  `internal/service/net/loop/batch_cost_internal_test.go`
- `internal/service/net/loop/uring_wait_linux_test.go` (`TestURingWaitArgs`,
  `TestURingWoke`)
