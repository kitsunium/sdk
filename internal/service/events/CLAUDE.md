# internal/service/events/

## Purpose

The concrete `core/events.Bus`: the copy-on-write membership, the ordered
insert, the synchronous dispatch with its panic guard and halt handling, and
the typed `On[E]` / `Off[E]` front end that hides the erasure. Admitted by
**ADR 0053**.

Code range: `0.3.52.*` (ADR 0053). The registration refusals are core sentinels
(`0.2.22.*`); this package owns only what the ENGINE decides at dispatch time.

## Contents

| File | Surface |
|---|---|
| `events.go` | `bus` (unexported) + `New() corev.Bus`, `Subscribe` / `Unsubscribe`, and the two validators |
| `state.go` | `state` — one published membership version — plus `cloneStateWith` / `cloneStateWithout` / `insertOrdered` |
| `publish.go` | `Publish`, `dispatch`, `classify`, `joinListenerFailure`, `call` (the panic guard) |
| `on.go` | `Handler[E]`, `On[E]`, `Off[E]`, and the unexported `erase[E]` |
| `codes.go` | `CodeListenerFailed` / `CodeHaltNotPermitted` — range 0.3.52.* |
| `errors.go` | `ListenerFailed` / `HaltNotPermitted` (`errs.Define`) |

## Why the registration is a FUNCTION and not a method

Go methods cannot take type parameters of their own. That is not a wrinkle to
route around — it settles the design. A method-shaped `Subscribe[E]` would put
the parameter on the receiver, and a `Bus[E]` carries exactly ONE event type,
which is a typed channel with a registry bolted on rather than a bus. So the
bus is erased (`Listener func(ctx, any) error`) and the typing lives in a
package-level generic:

```go
events.On(bus, events.Handler[OrderPlaced]{Name: "audit", Handle: …})
```

`On` does two things the caller would otherwise do by hand and get wrong:
it derives the routing key from `E` with `reflect.TypeFor[E]()`, and it wraps
the typed function in an erased one that asserts. Both come from the same
expression, so the key and the assertion can never disagree.

**The cost is measured, not asserted**: 2.4 ns per listener call against an
identical listener registered erased and never converting — 3.5 % of a
one-listener dispatch, see `BENCH.md` §Claim 1.

`erase`'s failure branch is unreachable through this package's API (`On` keys
on `E`, `Publish` only calls listeners filed under the event's dynamic type,
and an interface `E` is refused). It is still written, because a comma-ok that
is never false costs nothing while a silent zero value would cost a listener
firing on an event it never received.

## Why `kernel/snapshot` and NOT `kernel/topic`

`topic` was read and deliberately not used. It is the right primitive for a
different problem: every subscriber owns a **buffered channel** and a delivery
policy, so a publish is asynchronous by construction and returns a delivered
count. That shape cannot express this domain's two headline promises —

- **Synchronous completion.** `Publish` here must return when the last listener
  has returned. Over `topic`, `Publish` returns when the last value has been
  *handed to a buffer*, which is a different fact.
- **Propagation stop.** A listener's decision has to be observed BEFORE the
  next listener is called. Across a channel there is no "before": listener 2's
  copy is already in its buffer while listener 1 is still deciding.

Everything `events` actually needs from `topic` is the copy-on-write membership
underneath it, and that is `kernel/snapshot` — which `topic` itself is built
on. So this package takes the same primitive by the same reasoning and skips
the layer that would have to be defeated. Using `topic` here would have meant a
delivery policy nobody chose, a buffer per listener nobody reads, and a `Halt`
that arrives too late to mean anything.

## Conventions

- **The list is sorted at REGISTRATION, never at publication.** A bus is
  written once at wiring time and read once per event for the life of the
  process; sorting on the read path would allocate a scratch slice per
  published event, on the hottest path there is.
- **Ties keep registration order** because `insertOrdered` walks back over
  strictly-greater priorities only. No sequence number, no stable sort.
- **`Publish` never inspects `ctx`.** An event is a fact that already happened;
  abandoning half of its consequences because the publisher's deadline expired
  produces exactly the partial state the synchronous contract prevents.
  `TestACancelledContextStillDeliversEverything` is the guard.
- **A failure never short-circuits**, and the aggregate is `errors.Join` — the
  `service/lifecycle` shape, for the same reason: `errs.Wrap` would hit
  origin-wins and relabel the SDK's verdict with the listener's own code.
- **A panic is classified BEFORE a halt.** A crash is not a decision, so a
  listener that panicked never stops its siblings even when it holds `MayHalt`.
- **The panic's stack is captured in the deferred recover**, on the publisher's
  own goroutine. `kernel/group` needs a `PanicValue` type because it crosses a
  goroutine boundary; there is no boundary here, so the stack goes straight
  into an error field.
- **The recovered value is a FIELD, never the wrap origin**, so a
  `panic(someSentinel)` cannot hijack `LISTENER_PANICKED` — the
  `service/lifecycle.guard` precedent, with the same named test.
- **`New()` takes no configuration.** Every knob considered was a contract
  rather than a setting; a `Config` today would be an empty struct, which
  CLAUDE.md rule 5 refuses. A genuine knob arrives as a sibling constructor
  (ADR 0039).

## Do NOT

- **Dispatch on another goroutine, buffer, retry, or add a dead-letter path.**
  That is the `queue` domain wearing this one's name — see
  `internal/core/events/CLAUDE.md` §The frontier and ADR 0053 §D1.
- **Sort in `Publish`.** `BENCH.md`'s zero-allocation rows are the whole
  argument, and they are the first thing a read-path sort would cost.
- **Short-circuit the walk on a listener error.** It would make one listener's
  disk decide whether the others hear about the event.
- **Honour a `Halt` from a listener without `MayHalt`**, or swallow it. The
  first makes the permission decorative; the second leaves a listener convinced
  it vetoed an event that every sibling then saw.
- **Hold a lock across a listener call.** The membership is copy-on-write for
  exactly this reason, and
  `TestSubscribingDuringADispatchNeverPanicsOrDeadlocks` runs a listener that
  mutates the bus it is being dispatched from.

## Verification

```
bazel test --config=race //internal/service/events:events_test
# OR
cd internal/service && GOWORK=off go test -race -count=10 ./events

# benchmarks (regenerates the numbers in BENCH.md)
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./events/
```
