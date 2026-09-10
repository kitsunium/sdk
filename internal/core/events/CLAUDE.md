# internal/core/events/

## Purpose

Declares the **in-process event bus port**: `Listener` (react to one event),
`Bus` (dispatch a published event to the listeners registered for its type),
the `SubscriptionValue` / `DispatchValue` domain values, the `Priority` that
orders them, and the typed sentinels. Admitted by **ADR 0053**. The engine, the
ordered insert, the panic guard and the typed `On[E]` front end are concrete
and live in `internal/service/events`.

Code range: `0.2.22.*` (ADR 0053).

## The frontier — `events` is not `queue`

This is the first thing to read, and the reason the domain exists as a separate
one. The two are confused constantly, and a bus that tries to be both
guarantees neither.

| | `events` | `queue` |
|---|---|---|
| Scope | one process | many processes |
| Timing | synchronous | asynchronous |
| Goroutine | the publisher's | a consumer's |
| Transaction | the publisher's | its own |
| Durability | **none** | the point of it |
| Retries / DLQ | **none** | the point of it |
| Process dies mid-flight | the event never happened | the event is still there |

A caller who wants "asynchronous but reliable" is asking for a queue. This
domain will not give them a worse one: dispatching each listener on its own
goroutine would buy asynchrony while quietly removing durability — work that
can vanish with no record it existed. Publish an event AND enqueue a job; they
are different statements about different guarantees.

## Contents

| File | Surface |
|---|---|
| `events.go` | `EventType = reflect.Type`, `Listener func(ctx, any) error`, `Bus interface { Subscribe(EventType, SubscriptionValue) error; Unsubscribe(EventType, string) error; Publish(ctx, any) (DispatchValue, error) }` |
| `events_subscription.go` | `Priority` + `PriorityNormal`, `SubscriptionValue` — `Name` / `Priority` / `MayHalt` / `Listener` |
| `events_dispatch.go` | `DispatchValue` — `Delivered` / `Failed` / `Halted` / `HaltedBy` / `Skipped` |
| `codes.go` | `Code*` constants — range 0.2.22.* |
| `errors.go` | `InvalidSubscription` / `DuplicateListener` / `UnknownListener` / `InvalidEventType` / `ListenerPanicked` / `Halt` (`errs.Define`) |

## Conventions

- **No registry** — one dispatch discipline and one engine, so a registry would
  have exactly one entry and would add a way to misconfigure a wiring at
  runtime (`proc` ADR 0016, `resilience` ADR 0026, `scheduler` ADR 0041,
  `lifecycle` ADR 0050).
- **`Listener` is a FUNC type, not an interface.** The shape
  `internal/core/CLAUDE.md` already admits for `resilience.Operation`,
  `scheduler.Job` and `lifecycle.Start`. ADR 0039's rule is satisfied
  structurally: a published interface must not grow a method, and a func type
  **cannot**. `TestListenerIsAFunctionNotAnInterface` is the executable guard.
- **`Bus` is FROZEN at three methods.** `pkg/v1/events` aliases it, so the
  shape is published. `TestBusIsFrozenAtThreeMethods` and
  `TestATwoMethodDoubleDoesNotSatisfyBus` are the guards; a new capability gets
  a sibling interface reached by type assertion.
- **The routing key is the concrete Go TYPE**, hence `reflect.Type` in a core
  package. It is the one stdlib import here beyond `context`, and it is a
  VALUE type in the sense `time.Time` is in `lifecycle.TransitionValue` — an
  interface with no state, never a runtime engine. A name would be unchecked:
  two packages can both pick `"user.created"`, a rename silently unhooks every
  listener, and nothing fails until production goes quiet.
- **An interface `EventType` is refused, not starved.** `Publish` resolves an
  event's type with `reflect.TypeOf`, which yields a value's DYNAMIC type and
  never an interface type. Accepting such a subscription would list a listener
  that can never fire — ADR 0031's inert registration exactly.
- **Lower `Priority` runs first; ties run in registration order.** Both halves
  are promises, not implementation details.
- **`MayHalt` is an authority written at the registration site.** A veto nobody
  wrote down is one nobody reviews; the field puts it in the diff, the review
  and `grep` — the instrument ADR 0031 applies to
  `resilience.HedgeConfig.Idempotent`.
- **`Halt` is a control value, not a failure.** It is the `io.EOF` shape: a
  sentinel meaning "stop", matched with `errors.Is` / `errs.HasCode`, consumed
  by the bus and never returned to a publisher.
- **`DispatchValue` is five scalars.** A per-listener slice would allocate on
  every publish, including every publish where nothing went wrong; the
  per-listener detail is in the errors, which name the listener and the type.
- Registration refusals carry `EX_CONFIG` (78): the same `Subscribe` will be
  refused identically forever. `ListenerPanicked` keeps the default
  `EX_SOFTWARE` (70) — it is a fault in the listener, not in the wiring.

## Do NOT

- **Add a method to `Bus`, or turn `Listener` into an interface.**
  `pkg/v1/events` aliases both, so the shapes are published (ADR 0039).
- **Add an asynchronous delivery mode, a retry, a buffer or a dead-letter
  path.** Every one of them is the `queue` domain wearing this one's name, and
  each would deliver the appearance of reliability without any of it. See the
  frontier table above; ADR 0053 §D1 is a decision, not an omission.
- **Give `Halt` an exit code or an HTTP status.** It never reaches a caller,
  and a status on a control value invites somebody to render it.
- **Key a subscription on a string.** ADR 0053 §D2 records why, and
  `TestTwoEventTypesNeverCross` is what a name-keyed bus would eventually fail.
- **Let a panic be read as a halt.** A crash is not a decision;
  `TestAPanicIsNeverReadAsAHalt` fails on it, even for a listener that holds
  the authority.

## Verification

```
bazel test --config=race //internal/core/events:events_test
# OR
cd internal/core && GOWORK=off go test -race ./events
```
