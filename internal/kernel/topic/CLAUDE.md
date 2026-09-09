# internal/kernel/topic/

## Purpose

Generic **in-process broadcast**: `Topic[T any]` delivers every published value
to every current subscriber, typed, with multiple subscribers and clean
departure. A kernel primitive (stdlib-only — plus the kernel's own
`snapshot.Value` — AND generic: `Topic` / `Publish` / `Subscribe` / `Listener`,
with no `Event`, no `Message`, no `Handler` in a signature). Admitted on **SDK
rule 1**, the only admission criterion there is; the precedent is ADR 0025,
which admitted `kernel/cache` on that rule with **zero** consumers.

Emits **no error codes**: `Publish` returns a delivered count, and the one
programmer error it can detect — a subscription with no delivery policy —
panics.

## Contents

| File | Surface |
|---|---|
| `topic.go` | `Topic[T]` + `Subscribe` / `Publish` / `Subscribers` / `Close`, and `unregister` |
| `listener.go` | `Listener[T]` — one subscriber's handle, plus the three delivery paths |
| `delivery.go` | `DeliveryConfig` + `Block` / `DropOldest` / `DropNewest`, and the capacity floor |
| `state.go` | `state[T]` — one published version of the membership, and its two clones |

`Topic` has **no constructor**: its zero value is usable, as `sync.Mutex` and
`sync.Map` are. `Listener` has none for the opposite reason — every one comes
out of `Subscribe`, which is what binds the handle to its topic and refuses an
unset delivery policy.

## The decision that makes or breaks this primitive

**A slow subscriber must never stall the producer in silence.** That is why
there is no default delivery mode. Every `Subscribe` names one, at the call
site, by building a `DeliveryConfig`:

| Builder | On a full buffer | Costs |
|---|---|---|
| `Block(n)` | waits, bounded by `Publish`'s context and by the subscriber leaving | the coupling the caller asked for |
| `DropOldest(n)` | discards the STALEST buffered value, counted | a mutex per delivery (81.6 ns) |
| `DropNewest(n)` | discards the ARRIVING value, counted | one non-blocking send (20.3 ns) |

`DeliveryConfig` has **no usable zero value** — ADR 0031 applied by making the
bad state unspellable rather than merely checked. Its fields are unexported so a
`DeliveryConfig{}` literal cannot be written, and a `var d DeliveryConfig` is
refused by `Subscribe`, at the call that omitted it. Whatever a zero meant it
would be a backpressure decision the caller never made, and the two candidates
are the exact failures this primitive exists to prevent: a silent stall
(`Block`) or a silent loss (`Drop*`).

Silence is closed off from both ends. With a drop policy the loss is **counted**
and readable through `Listener.Dropped()`. With `Block` the coupling is written
at the call site and bounded by `Publish`'s context, and a `Publish` that gave
up counts the value as dropped — so `Block` is a coupling, never a hang.

**The buffer depth clamps rather than refuses** (`< 1 → 1`), and the asymmetry
is the ADR's own line: the depth is a *floor* the SDK can supply ("hold at least
one value"), exactly as the rate limiter clamps `Burst` to "admit at least one";
the *policy* is the caller's intent, and that is refused when absent.

## Leaving during a fan-out

`Listener.Unsubscribe()` is safe at any moment, **including while a `Publish` is
delivering to that very subscriber**, and it never costs another subscriber its
copy. Two decisions buy that:

1. **`Publish` reads the membership from a copy-on-write snapshot and delivers
   outside every lock.** Holding a lock across a delivery would let one blocked
   subscriber freeze `Unsubscribe` — including the `Unsubscribe` that would have
   unblocked it. That is the classic deadlock in this shape of code, and the
   subscriber goroutine that stopped reading is usually the one unsubscribing.
2. **The value channel is NEVER closed.** `Unsubscribe` closes a separate `done`
   channel, which releases a publisher parked on a `Block` delivery. Closing the
   value channel instead would put "send on closed channel" one race away, and
   that panic lands in the **publisher** — code that did nothing wrong. Here it
   is unreachable rather than unlikely.

The cost of (2) is stated rather than hidden: ranging over `Listener.Values()`
never ends. Select on `Listener.Done()` alongside it, exactly as with a context.
That is also what `Topic.Close()` signals.

## Why `snapshot.Value` and not an RWMutex + map

The obvious implementation copies the subscriber list under a read lock so
delivery can happen outside it. It is correct, and it **allocates a slice per
published value** — on the hottest path there is. Holding the membership in
`kernel/snapshot.Value[state[T]]` makes `Publish` one atomic pointer load and a
range: 0 B / 0 allocs on every `Publish` row in `BENCH.md`. The cost moves to
membership churn (642 ns / 672 B per join+leave against 16 members), which is
the better half of the trade for a list set up once and read per value — and the
number is published so the opposite case is a decision rather than a discovery.

The `closed` flag lives **inside** the snapshot rather than beside it, so
closing and subscribing serialise on the one writer lock. A separate
`atomic.Bool` would let a `Subscribe` racing a `Close` register into a list that
has already been abandoned.

## Conventions

- **`Publish` returns a delivered count.** Below `Subscribers()` it means
  something did not land: a drop, a departure, or an expired context. It is
  never zero-information.
- **A departure is not a drop.** A subscriber that left is skipped and counted
  as neither delivered nor dropped — conflating the two would make a clean
  shutdown look like a capacity problem.
- **Ordering**: values from ONE publisher reach each subscriber in publication
  order. Concurrent publishers interleave in an unspecified order, and so does
  the order in which subscribers are visited within one `Publish`.
- **`Subscribe` after `Close` hands back an already-done handle**, not a panic
  and not an error: a shutdown race then reads as a shutdown at the call site.
- **`Close` does not drain.** Values already delivered stay readable.
- **`DropOldest` takes a per-subscriber mutex.** Its receive-then-send pair must
  not interleave with another publisher's, or both would evict and only one
  would land — a loss neither could attribute.
- Cross-OS: 100 % portable (`context`, `sync`, `sync/atomic`, plus
  `kernel/snapshot`).

## Do NOT

- Close the value channel, in `Unsubscribe` or in `Close`. It converts a safe
  departure into a race whose panic lands in the publisher;
  `TestConcurrentPublishAndUnsubscribeNeverPanics` fails on it under `-race`.
- Give `DeliveryConfig` a usable zero value, or default it inside `Subscribe`.
  `TestSubscribeWithoutADeliveryPolicyRefuses` and
  `TestTheZeroDeliveryConfigIsTheUnsetMode` are the two guards.
- Deliver while holding the membership lock. See §"Leaving during a fan-out";
  `TestUnsubscribeReleasesAPublisherBlockedOnThatSubscriber` deadlocks on it.
- Add error codes. The delivered count and `Dropped()` carry everything a caller
  can act on.
- Expect cross-process broadcast. A `Topic` reaches the subscribers in one
  process and nothing beyond it.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./topic/
bazel test --config=race //internal/kernel/topic:topic_test
```
