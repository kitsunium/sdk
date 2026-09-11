<!-- generated from internal/kernel/topic/topic_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./topic/` to refresh -->
# Benchmarks — `internal/kernel/topic`

Typed in-process broadcast. Two questions decide whether this primitive is worth
its overhead, and both are measured here: **what does a fan-out cost per
subscriber**, and **what does the delivery policy cost when a subscriber is
behind**.

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
BenchmarkPublish_NoSubscribers-8              394817548     2.990 ns/op     0 B/op   0 allocs/op
BenchmarkPublish_OneDrainedSubscriber-8        16719366    60.47  ns/op     0 B/op   0 allocs/op
BenchmarkPublish_EightDrainedSubscribers-8      1000000  1323     ns/op     0 B/op   0 allocs/op
BenchmarkPublish_SaturatedDropNewest-8         61546912    20.31  ns/op     0 B/op   0 allocs/op
BenchmarkPublish_SaturatedDropOldest-8         14724246    81.57  ns/op     0 B/op   0 allocs/op
BenchmarkSubscribeUnsubscribe-8                 1903035   642.2   ns/op   672 B/op   7 allocs/op
BenchmarkBaseline_DirectChannelSend-8          79376113    14.65  ns/op     0 B/op   0 allocs/op
```

## Publish allocates nothing, ever

**Every `Publish` row is 0 B / 0 allocs.** That is the point of holding the
membership in a `kernel/snapshot.Value` rather than in a map behind an
`RWMutex`: the fan-out path is one atomic pointer load and a range over a slice
nobody may mutate. The obvious alternative — copy the subscriber list under a
read lock so delivery can happen outside it — is correct but allocates a slice
per published value, on the hottest path there is.

The cost is moved to membership churn instead, and that is
`BenchmarkSubscribeUnsubscribe`: **642 ns and 672 B for one join+leave against a
resident membership of 16**, because each rebuilds the whole list. A topic whose
membership is set up once and read millions of times gets the better half of
that trade; one that churns membership per published value would not, and the
number is here so that case is a decision rather than a discovery.

`BenchmarkPublish_NoSubscribers` at 2.99 ns is the floor — an atomic load and a
nil check. It is the common state of a diagnostic topic in production, and it is
close enough to free that leaving one wired up costs nothing.

## What a fan-out costs per subscriber

| Subscribers | ns/op | per subscriber |
|---|---|---|
| 0 | 2.99 | — |
| 1 | 60.5 | 57.5 |
| 8 | 1323 | 165 |

The marginal cost **rises** with membership, from ~58 ns to ~165 ns per
subscriber, and the reason is not the loop — it is the scheduler. Each delivery
is a channel send that may wake a parked reader, and with eight readers on eight
cores the publisher spends most of its time in `runtime.goready` rather than in
this package. Any broadcast built on channels behaves this way; the number is
here so a caller sizing a topic at hundreds of subscribers knows to expect
scheduler-bound scaling rather than a flat per-member cost.

`BenchmarkBaseline_DirectChannelSend` (14.65 ns) is one non-blocking send to one
buffered channel with no topic at all. One subscriber costs **4× that**, which
buys the copy-on-write membership, the departure check, and the drop accounting.
Below one subscriber there is nothing to broadcast to, and a plain channel is
the right answer.

## What the delivery policy costs

The two saturated rows are the same subscriber, permanently behind, with
different policies:

| Policy | ns/op | what it does when full |
|---|---|---|
| `DropNewest` | 20.3 | one failed non-blocking send, one counter increment |
| `DropOldest` | 81.6 | a mutex, a failed send, a receive, a second send |

**Keeping the freshest window costs 4× keeping the earliest one.** The mutex is
not optional: without it two publishers can interleave their receive-then-send
pairs, each evicting one value and only one landing — a loss neither could
attribute. That trade is the reason both policies exist rather than one, and the
reason `DropNewest` is the cheaper default to reach for when either would do.

Both remain far below `Block`, which is not benchmarked because its cost is
whatever the slow subscriber decides: a `Block` delivery to a full buffer parks
the publisher until a reader drains it, [Topic.Publish]'s context expires, or the
subscriber leaves. That is a coupling the caller asked for at the call site, and
timing it would only measure the test's own sleep.

## What is deliberately not measured

- **The Block policy under saturation.** See above — the number would be the
  benchmark's own timeout, not this package's cost.
- **Unsubscribe during a live fan-out.** It is a correctness property (the other
  subscribers keep their copy, nothing panics), pinned by
  `TestUnsubscribeMidFanOutDoesNotCostTheOthersTheirCopy` and by a `-race` stress
  test, not a throughput one.
- **Cross-process broadcast.** There is none. A `Topic` reaches the subscribers
  in one process and nothing beyond it.
