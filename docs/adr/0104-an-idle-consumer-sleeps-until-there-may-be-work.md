# ADR 0104 — an idle consumer sleeps until there may be work

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0054](0054-sdk-queue-domain.md) (`Consume` stops being a pure poll)
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings)

## Context

`Consume` finds work one way: it calls `Receive`, and when the queue is empty it
waits `PollInterval` and asks again. `Receive` cannot block — the port says so,
and the durable broker's state is a directory nobody can wait on — and nothing
could wake the wait early. So a consumer's latency and its idle cost were one
knob. A downstream framework ran a dozen consumers at 50 ms and measured about
3 % of a core spent doing nothing. Measured here, twelve idle consumers for ten
seconds, darwin/arm64:

| broker | poll 50 ms | poll 5 s |
|---|---|---|
| memory | 0.23 % of a core | 0.00 % |
| file (two directory reads per poll) | **5.19 %** | 0.06 % |

The five-second column is cheap and useless while nothing wakes the consumer: a
message published a millisecond after a poll waits five seconds. And it is not
only publications that are missed. A nacked message becomes visible
`RetryDelay` later and a dead consumer's lease lapses `VisibilityTimeout` later;
nothing happens at either instant, so no signal could report it, and a long
poll delays every retry by up to its whole length.

## Decision

### D1 — a sibling port, `Waker`

`core/queue.Waker` has one method, `Wake() WakeValue`. `Broker` keeps its four
methods (ADR 0039): a broker that cannot say when to look is simply polled, as
before. `WakeValue` carries two things:

- **`Signal`**, a channel closed by the next event that may make a message
  receivable through this broker in this process — a `Publish`, or a `Nack`
  that hands one back. Closed and replaced, never sent on: a close wakes every
  waiter, a send wakes one or nobody. It is never nil.
- **`In` and `Scheduled`** — how long until a message the broker already holds
  becomes receivable with no further event: the head of the ready list whose
  retry delay is ending, or the earliest lease, whose holder may be dead.

`In` is a DURATION read on the broker's clock, never an instant. The broker and
the consumer each take a clock and nothing makes them one; an instant compared
across two clocks that disagree can sit permanently in the past and spin the
loop, while a duration is just waited, on the consumer's clock.

### D2 — `Consume` waits on it, with no lost wake-up

Each worker takes `Wake().Signal` BEFORE its `Receive`. A publication landing
between a `Receive` that came back empty and the wait has then already closed
the channel the worker holds, so the wait returns at once; there is no window.
After the empty `Receive` it reads `Wake()` again for `In`, which now reflects
what that `Receive` saw, and waits for the first of: its context, the signal,
or `min(PollInterval, In)` on its own clock — through `NewTimer`, stopped when
something else wins, so a `ManualClock` is not left holding armed waits.

`PollInterval` keeps its meaning and its default of 100 ms, and becomes a
BOUND rather than a cadence: what is left for it to find is a publication made
by ANOTHER process into a durable queue, which nothing in this process can see.
A caller with no such producer raises it by a hundredfold and loses no latency;
the package doc shows five seconds.

### D3 — both brokers answer

- **The memory broker** reads `In` from the structures `Receive` uses, under its
  own lock: the ready list is ordered by visibility and the expiry heap by
  deadline, so both answers are their heads. A heap entry an `Ack` has made
  stale only wakes a consumer early, to a `Receive` that discards it.
- **The file broker** cannot read a directory without a `Receive`, and does
  not try: each `Receive` RECORDS what its two scans already stop on — the
  first ready name not yet visible, the first lease not yet lapsed — plus the
  deadline of any lease it just took, and `Wake` reads the record. It can be
  stale only in the harmless direction (another process took the message, a
  lease was extended), which costs one empty `Receive` that records afresh.
- **Two durable brokers over one directory share one signal.** They are one
  queue (`FileConfig.Dir` says so), so a producer holding one value and a
  consumer holding another — the ordinary shape when they are two components —
  must still wake. The signal lives in a process-wide table keyed by the
  directory resolved through its links, held WEAKLY, with a
  `runtime.AddCleanup` that drops the entry once no broker references it, so a
  process opening queues in fresh directories does not keep one entry per
  directory forever.

Neither broker gains a timer or a goroutine: the wait is the consumer's, and
the broker only answers.

## Consequences

- Measured above: at a five-second poll, twelve idle consumers cost 0.06 % of
  a core on the durable broker instead of 5.19 % at 50 ms, and a publication,
  a nack, a retry falling due and a lease lapsing are each handled at their own
  instant rather than at the next poll — pinned with a clock that never moves.
- A publication from another process is still found by polling, at
  `PollInterval`; that is the one thing the bound is for, and the doc says so.
- A consumer woken for nothing — a message another consumer took — pays one
  empty `Receive`, which is what it paid on every poll before.

## Breaking changes

None. `Waker` and `WakeValue` are new; `Broker`, `Handler` and every signature
are unchanged; `DefaultPollInterval` is still 100 ms, so a caller that set
nothing keeps its cadence and gains the wake.

## Alternatives considered

- **A blocking `Receive`, or a fifth method on `Broker`.** ADR 0039 forbids the
  fifth method, and a durable broker has nothing to block on but a timer — the
  poll, moved.
- **A signal without `In`.** A long poll would then delay every retry and every
  recovery from a dead consumer by up to its whole length, and the knob would
  still trade latency for idle cost — for everything except publications.
- **An instant instead of a duration.** Two clocks, and a comparison between
  them that can be permanently false.
- **One signal per broker VALUE.** Two brokers over one directory in one
  process would not wake each other, although they are one queue.
- **A filesystem notification (inotify, kqueue).** It would reach across
  processes, and it is a platform-specific mechanism per GOOS, with its own
  failure modes, for the one case the poll already covers.

## Deferred

- **Cross-process wake-ups for the durable broker.** A publication in another
  process is found by the poll. Anything better is a notification mechanism
  per platform (ADR 0018), and belongs to a change that measures it.
- **The directory table's key is a spelling.** Two spellings the table cannot
  reconcile — a hard-linked directory, a bind mount — do not share a signal and
  fall back to the poll.

## Verification

- `internal/service/queue/wake_external_test.go`, over BOTH brokers with a
  `ManualClock` that never moves unless the test moves it and a poll of an
  hour: a publication wakes an idle consumer with the clock untouched; a nacked
  message is redelivered after exactly `RetryDelay` of clock; a lease held by a
  consumer that "died" is recovered after exactly `VisibilityTimeout`; two
  durable brokers over one directory, and a third over a symbolic link to it,
  wake each other; `Wake` reports nothing scheduled on an empty broker, closes
  its signal on `Publish` and `Nack`, and reports the retry due in
  `RetryDelay`.
- `internal/service/queue/wake_internal_test.go` — `earliest`, the broadcast,
  and the table dropping a collected signal's entry.
- `pkg/v1/queue/queue_external_test.go` — both brokers carry `Waker`, and a
  consumer with an hour's poll handles a publication through the facade.

## References

- [ADR 0054](0054-sdk-queue-domain.md) — the port, the two brokers, `Consume`.
- [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) — siblings, not a fifth method.
- [`runtime.AddCleanup`](https://pkg.go.dev/runtime#AddCleanup), [`weak`](https://pkg.go.dev/weak) — the table's lifetime.
