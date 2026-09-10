# events

Package `events` declares the SDK **in-process event bus** port: the `Listener`
that reacts to one event, the `SubscriptionValue` that registers it at a
priority, the `DispatchValue` that reports one publish, and the `Bus` that
drives them — plus the typed sentinels a registration is refused with
(InvalidSubscription / DuplicateListener / UnknownListener / InvalidEventType /
ListenerPanicked) and the `Halt` control sentinel a listener returns to stop a
dispatch.

**`events` is not a queue.** It is in-process, synchronous and same-goroutine:
`Publish` returns when every listener has returned, there is no durability, no
retry and no dead-letter path, and if the process dies mid-dispatch the event
never happened. A caller who needs "asynchronous but reliable" needs a queue and
must be sent to one rather than served half of it here.

`Listener` is a function port rather than an interface, so it cannot grow a
method and break a downstream implementer (ADR 0039). `Bus` is frozen at three
methods for the same reason; a new capability gets a sibling.

Subscriptions are keyed on the event's **concrete Go type**, not on a name: the
compiler mints the key, so two packages cannot both spell `"user.created"`. An
interface event type is refused at registration, because `Publish` routes on a
value's dynamic type and such a listener could never fire (ADR 0031).

The engine, the ordering, the panic guard and the typed `On[E]` front end live
in `internal/service/events`; facade: `pkg/v1/events`. ADR 0053. See
`CLAUDE.md`.
