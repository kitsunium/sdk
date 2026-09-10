# ADR 0053 — in-process event bus domain (`events`), and the line between it and `queue`

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (4-layer shape), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (error codes), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings, never by widening), [ADR 0050](0050-sdk-lifecycle-domain.md) (`lifecycle`, the previous assembly domain), [ADR 0016](0016-sdk-process-supervision-domain.md) / [ADR 0026](0026-sdk-resilience-domain.md) / [ADR 0041](0041-sdk-scheduler-domain.md) (the no-registry precedents)

## Context

Every application built on this SDK eventually reaches the same shape: one fact
happens, and several unrelated things must happen because of it. An order is
placed, and an audit row is written, a cache tag is invalidated, and a counter
is incremented. The three have nothing to say to one another, they will not be
the same three next quarter, and the function that placed the order should not
import any of them.

The SDK had every piece of that and no assembly of it. `cache` can invalidate,
`metrics` can count, `logger` can record — and the only way to make one call
site do all three was to import all three.

### The confusion this domain exists to survive

An in-process event bus and a message queue are described with the same words —
publish, subscribe, listener, event, handler — and they guarantee opposite
things. That is not a naming annoyance. It is the failure mode:

> The library that "supports both" reliably guarantees neither. It dispatches
> on a goroutine, so it is not synchronous; it holds nothing on disk, so it is
> not durable; and it reports success the moment the value entered a buffer, so
> the caller believes a thing happened that may never happen.

A caller who reaches for a bus when they needed a queue does not find out at
compile time, in review, or in staging. They find out when a process restarts
under load and a day of side effects is missing, with no record that anything
was lost — because the only record was a `Publish` that returned `nil`.

A second domain, `queue`, is planned. This ADR's first job is to draw the line
before either is written, so neither drifts into the other's territory.

### `kernel/topic` had just landed

`internal/kernel/topic` shipped immediately before this work: generic typed
broadcast, multiple subscribers, an unwritable-by-omission overflow policy, a
zero-allocation `Publish`, safe departure mid-fan-out. It looks like the
primitive this domain is built on. It was read in full, and it is not — for
reasons that turn out to BE the domain's definition. See §D9; the answer is
recorded rather than left as an absence, because "why didn't you use the thing
you just built" is the first question a reader will have.

## Decision

### D1 — `events` is in-process, synchronous and same-goroutine. It is not a queue, and it will not become half of one.

| | `events` | `queue` |
|---|---|---|
| Scope | one process | many processes |
| Timing | synchronous | asynchronous |
| Goroutine | the publisher's | a consumer's |
| Transaction | the publisher's | its own |
| Durability | **none** | the point of it |
| Retries / DLQ | **none** | the point of it |
| Process dies mid-flight | the event never happened | the event is still there |

`Publish` returns when the last listener has returned. There is no buffer, no
worker, no `go` statement anywhere in the dispatch path, and there will not be
one. **A caller who wants "asynchronous but reliable" is asking for a queue and
must be sent to one**, not served the asynchrony without the reliability.

The tempting middle — "dispatch each listener on its own goroutine, it is one
line" — is the exact defect described above. It converts a synchronous
guarantee the caller can reason about into an asynchronous one the SDK cannot
honour: the work is now off the caller's transaction, off their error path, and
gone entirely if the process exits. The right shape for a caller who needs both
is two statements: publish the event AND enqueue the job. They are different
claims about different guarantees, and writing them separately is the only way
a reader can tell which one was made.

This is recorded here rather than in a comment because it is the decision most
likely to be re-litigated, by someone holding a profile and a good intention.

### D2 — The routing key is the event's concrete Go TYPE, not a name.

A subscription is filed under `reflect.Type`. `Publish` resolves the published
value's dynamic type and dispatches to that list.

A string name — `"order.placed"` — is the obvious alternative and is unchecked
in every direction that matters. Two packages can both pick `"user.created"`. A
rename in one of them silently unhooks every listener. A typo produces a
listener that is registered, listed, and never called. **Nothing fails until
production goes quiet**, which is the worst possible latency for this class of
bug.

A Go type is minted by the compiler, cannot collide across packages, and is
renamed by the same tool that renames its uses. The price is stated: an event
that crosses a process boundary has no identity in this scheme — which is not a
limitation to work around, it is §D1.

`reflect.Type` therefore appears in a core package. It is stdlib, it is an
interface with no state, and it sits in a signature the way `time.Time` sits in
`lifecycle.TransitionValue` — a value, never a runtime engine. `internal/core`'s
rule bans *concrete stateful runtime types*, and this is not one.

**An interface event type is refused at registration** (`InvalidEventType`,
`0.2.22.4`). `reflect.TypeOf` returns a value's DYNAMIC type and never an
interface type, so `On[error]` or `On[any]` would register a listener that is
accepted, listed by every introspection, and called exactly never. That is ADR
0031's inert registration in its purest form, and it refuses at the call that
made the mistake.

### D3 — Typing: the port is erased, and the typing lives in a package-level generic. Measured at 2.4 ns.

Go has no covariance, and — decisively — **Go methods cannot take type
parameters of their own**. That single rule settles the design:

- A method-shaped `bus.Subscribe[E](…)` cannot be written.
- Pushing the parameter onto the receiver gives `Bus[E]`, which carries exactly
  **one** event type. That is a typed channel with a registry bolted on, not a
  bus, and an application with 30 event types would hold 30 of them plus the
  wiring to find the right one — reinventing the routing this domain exists to
  provide, without the ordering or the halt.

So the port is erased — `Listener func(ctx context.Context, event any) error` —
and the typing is restored by a package-level generic function:

```go
events.On(bus, events.Handler[OrderPlaced]{
	Name:     "audit",
	Priority: -10,
	Handle:   func(ctx context.Context, e OrderPlaced) error { … },
})
```

`On` derives the routing key with `reflect.TypeFor[E]()` and wraps the typed
function in an erased one that asserts. Both come from the same expression, so
the key and the assertion cannot disagree. **The caller's listener never sees
an `any`.**

**The cost is measured, not asserted.** `internal/service/events/BENCH.md`
§Claim 1 compares the typed path against an identical listener registered
erased and never converting — same dispatch, same guard, same lookup, same
listener body — over ten runs at `-benchtime=2s`:

| Path | mean | min | max |
|---|---|---|---|
| erased, no assertion | 67.44 ns | 66.46 | 68.66 |
| typed, one assertion | 69.85 ns | 68.40 | 71.64 |

**2.4 ns per listener call — 3.5 % of a one-listener dispatch.** The ranges
barely touch, so it is signal rather than noise, and it is one comparison of
two type pointers, which is what it should be. A number that small is what
makes "erase and restore" the right answer rather than a resignation.

One cost the domain cannot remove is stated in the same file: **boxing the
event into the `any` that `Publish` takes costs 16 B and one allocation at a
real call site** (96.6 ns vs 72.6 ns for a loop-invariant event the compiler
hoists). Every other row in `BENCH.md` reports `0 allocs/op` and would mislead
anyone sizing a hot publisher, so the honest number is measured and published
next to them.

### D4 — Ordering: ascending priority, ties by registration order, sorted at registration.

Lower `Priority` runs first, as with every stdlib comparator. A listener that
must observe an event before the others registers negative; one that must run
after them registers positive. `PriorityNormal` is the zero value and is the
right value for a caller with no opinion — ADR 0031's clamp branch: the SDK can
supply "no opinion" without inventing the caller's requirement, so there is
nothing here to refuse.

**Two listeners at the same priority run in the order they were registered.**
That is a promise, not an implementation detail. A bus whose ties resolved by
map iteration would run the same program in a different order on every
execution, and a listener set that works on Tuesday would be the same code that
fails on Wednesday. It is asserted on the observable trace, twice: once on
eight same-priority listeners, and once across ten consecutive publishes,
because one correct dispatch proves nothing about the next.

The list is kept sorted **at registration** rather than at publication. A bus is
wired once and published to for the life of the process; sorting on the read
path would re-derive on every event an order the insert already knows, and would
allocate a scratch slice per published event on the hottest path there is.

### D5 — Stopping propagation is an AUTHORITY, declared at the registration site.

A listener stops the dispatch by returning the `Halt` sentinel
(`0.2.22.6`); the listeners after it in priority order are not called.

**Only a subscription with `MayHalt: true` may do so.** From any other listener
the attempt is `HaltNotPermitted` (`0.3.52.2`), the dispatch **continues**, and
the refusal is collected with the other failures.

Two questions, answered separately:

**Who can stop?** Whoever the wiring says can. A veto is an authority, and an
authority nobody wrote down is one nobody reviews. Making it a field puts it in
the diff, in the review, and in `grep`, next to the name of the listener that
holds it — the same instrument ADR 0031 applies to
`resilience.HedgeConfig.Idempotent`, for the same reason: a precondition the
SDK cannot check becomes a required, greppable assertion instead of a comment.
Its zero value is `false`, which is the conservative reading, so a listener
cannot acquire a veto by omission.

The two failure modes were weighed and both are refused. Honouring an
unauthorised halt anyway makes `MayHalt` decorative. Swallowing it silently is
worse: it leaves a listener convinced it vetoed an event that every one of its
siblings then saw, and that divergence is invisible until the consequences
disagree.

**How is it visible on reading?** Three places, and all three are needed:

- at the listener: `return events.Halt` — a named sentinel, not a bare `nil`;
- at the wiring: `MayHalt: true` — the only place the authority is granted;
- in the report: `Dispatch.Halted`, `Dispatch.HaltedBy` and `Dispatch.Skipped`
  — the whole answer to "why did the last three listeners not run", which is
  otherwise a question only a debugger can settle.

`Halt` is a sentinel `error` rather than a second return parameter because the
error slot is already in the signature, and widening `Listener` to
`func(ctx, any) (bool, error)` would break every listener ever written against
it — ADR 0039's rule applied to a shape rather than to a method set. The
stdlib's `io.EOF` is the same instrument: a value meaning "stop", matched with
`errors.Is`, never rendered to a user. It is consumed by the bus, so a halt
returns a **nil** error from `Publish`: stopping the dispatch is a decision the
wiring asked for, and reporting a design as a failure would make every caller
who uses the mechanism write a special case to ignore their own intent.

### D6 — A bus with no listener is legitimate; an unusable registration is refused.

Publishing into a bus nobody listens to returns the zero `DispatchValue` and a
nil error. **A publisher must not have to know whether anybody is listening** —
that is the entire point of publishing. It costs 32.9 ns and zero allocations
(`BENCH.md` §Claim 3), which is what makes the rule usable rather than a
slogan.

What *is* refused is a registration that could never run, at the call that made
the mistake (ADR 0031's second branch): an empty name or a nil listener
(`InvalidSubscription`), a name already taken for that event type
(`DuplicateListener`), an interface or nil event type (`InvalidEventType`).
`Unsubscribe` on a name nobody holds is `UnknownListener` rather than a silent
success — "removed" and "there was nothing to remove" are different answers,
and a caller who gets the second while expecting the first has a bug that
silence would hide until the listener fired again.

Every refusal carries `EX_CONFIG` (78): the same call will be refused
identically forever, and retrying it is pointless.

### D7 — A listener error never stops the dispatch, and `ctx` is never inspected.

**Errors aggregate with `errors.Join` and the walk continues.** Listeners are
INDEPENDENT consequences of one fact; making the third one's delivery depend on
the second one's disk being full would reintroduce exactly the coupling a bus
exists to remove. Ignoring them is the other wrong answer — a bus is not a place
errors go to die — so everything that failed is reported.

Each failure carries the bus's own verdict, `ListenerFailed` (`0.3.52.1`), and
the listener's error **side by side** rather than one wrapping the other. This
is `service/lifecycle`'s shape and the reason is the same: `errs.Wrap` would hit
the origin-wins rule (CLAUDE.md rule 6) and inherit the listener's code, so "did
any listener fail?" would only be answerable by someone who already knew every
code every listener might produce. Joined, `errs.HasCode(err,
CodeListenerFailed)` and the caller's own `errors.Is` both answer on the same
value.

**`ctx` is passed to every listener and never read by the bus.** An event is a
fact that has already happened; abandoning half of its consequences because the
publisher's deadline expired produces precisely the partial state the
synchronous contract exists to prevent. The price is stated rather than hidden:
a dispatch does not end early, so a listener that must honour cancellation has
to do it itself — and a caller who wants the bus to return before the work
finishes wants §D1's other column.

### D8 — A listener panic is captured, reported, and the walk continues.

A panic escaping a listener would kill the **publisher** — which, in a
synchronous same-goroutine bus, is the caller's request or transaction, and is
the one party that did nothing wrong. It would also silently cancel every
listener after it, letting a single broken observer decide what the rest of the
application gets to see.

So it is recovered on the spot and becomes `ListenerPanicked` (`0.2.22.5`),
carrying the listener's name, the event type, the panic value and **the
originating stack** in fields. The dispatch then continues: the listener that
crashed is broken, the ones after it are not.

`kernel/group.PanicValue` was read and is the precedent — with the part that
does not transfer named. `group` needs a dedicated type because a panic there
crosses a **goroutine boundary**: `recover()` in the parent cannot see it, and
a stack taken later belongs to the wrong frame. There is no boundary here; the
listener ran on the publisher's goroutine, so `debug.Stack()` inside the
deferred recover already names the failing frames, and the value becomes an
ordinary typed error instead of a second `PanicValue` type. `group` keeps
`PanicValue` non-`error` on purpose — a fault must not be quietly ignorable —
and that argument does not apply either, because here the fault is aggregated
into a return value the caller is already obliged to check.

Two rules fall out and are guarded by name:

- **The recovered value travels as a FIELD, never as the wrap origin**, so a
  `panic(someSentinel)` cannot hijack `LISTENER_PANICKED` (the
  `service/lifecycle.guard` precedent).
- **A panic is classified before a halt.** A crash is not a decision, so a
  listener that panicked never stops its siblings — *even when it holds
  `MayHalt`*.

### D9 — `kernel/topic` was read and deliberately not used.

`topic` gives every subscriber a **buffered channel** and a delivery policy, so
a publish is asynchronous by construction and returns a delivered count. That
shape cannot express either of this domain's headline promises:

- **Synchronous completion.** `Publish` here must return when the last listener
  has RETURNED. Over `topic` it returns when the last value has been handed to
  a *buffer*, which is a different fact — and the difference is exactly the
  reliability illusion §D1 refuses.
- **Propagation stop.** A listener's decision has to be observed BEFORE the
  next listener is called. Across a channel there is no "before": listener 2's
  copy is already in its buffer while listener 1 is still deciding, so `Halt`
  would arrive too late to mean anything.

Everything `events` actually needs from `topic` is the copy-on-write membership
underneath it — and that is `kernel/snapshot`, which `topic` itself is built
on. So this domain takes the same primitive by the same reasoning and skips the
layer it would have had to defeat. Using `topic` here would have bought a
delivery policy nobody chose, a buffer per listener nobody reads, and a veto
that cannot work.

`topic` remains the right primitive for its own problem — fan-out to
independent readers that own their own goroutines — and the SDK is better for
having both rather than one that half-fits twice.

The copy-on-write membership is what makes a listener able to `Subscribe` or
`Unsubscribe` from **inside the dispatch that is walking the list**, with no
deadlock and no race: `Publish` is one atomic load and a range over a slice
nobody will mutate. `TestSubscribingDuringADispatchNeverPanicsOrDeadlocks` runs
exactly that, and the `-race` churn test runs it concurrently.

### D10 — No registry, no `Config`, four layers.

**No registry**, for the reason `proc`, `resilience`, `scheduler` and
`lifecycle` give: there is one in-process dispatch discipline, so a registry
would have exactly one entry and would add a way to misconfigure a wiring at
runtime.

**`New()` takes no configuration.** Every knob considered was a CONTRACT rather
than a setting — whether a listener error stops the dispatch, whether a
cancelled context abandons it, who may halt it — and a bus whose semantics vary
by construction is a bus no call site can be read against. A `Config` today
would be an empty struct, which CLAUDE.md rule 5 refuses; a genuine knob arrives
as a sibling constructor, which is what ADR 0039 prescribes for a signature that
cannot grow.

**No new kernel primitive.** `kernel/snapshot` (ADR 0011) already is the one
this needed.

Layers: `internal/core/events` (contract + values + refusals, `0.2.22.*`),
`internal/service/events` (engine + typed front end, `0.3.52.*`),
`pkg/v1/events` (aliases + three delegations). One real implementation, which
is the only kind this SDK ships.

## Consequences / Semantics

- **A publisher stops importing its consequences.** The function that placed
  the order publishes `OrderPlaced` and knows nothing about audit, cache or
  metrics. The wiring that knows about all four lives in one place, where it
  can be read.
- **The dispatch order is a property of the wiring, and it is stable.**
  Ascending priority, ties by registration. Two runs of the same program
  produce the same order.
- **`Publish` returns two things and both are load-bearing.** The
  `DispatchValue` says what happened to the dispatch — delivered, failed,
  halted, by whom, how many skipped. The error says what went wrong and to
  whom. A halt appears only in the first.
- **A halt is nil-error.** Code that treats any non-nil error from `Publish` as
  a problem stays correct, and code that uses the halt mechanism does not have
  to special-case its own design.
- **The failing path allocates and the succeeding one does not.** 0 B / 0
  allocs for a dispatch where nothing goes wrong; 384 B / 7 allocs when one
  listener fails. The `DispatchValue` is five scalars for exactly this reason —
  a per-listener slice would have allocated on every publish, including the
  overwhelmingly common one.
- **The caller pays 16 B to box their event.** Measured, published, and not
  removable while one bus carries many types.
- **`errs.HasCode(err, events.CodeListenerFailed)` answers "did anything fail",
  and `errors.Is` still finds the caller's own sentinel.** Both, on the same
  value.
- **A `queue` domain can be added later without renegotiating anything here.**
  The frontier table is the contract between them, and it is written down
  before either could drift.

## Breaking changes

None. `events` is a new domain in this change set: no existing package changed,
no published shape moved, and the ADR 0040 v0 licence is not used. The only
edits outside the three new packages are the two `codeRangeOwners` rows, the
three `audit_srcs` entries, the regenerated `docs/error-codes.yaml`, and the
documentation the change is obliged to keep in step (rule 11).

## Alternatives considered

- **A generic `Bus[E]`.** Rejected in §D3: it carries one event type, so an
  application holds one bus per type plus the routing to pick between them —
  which is this domain, hand-rolled and without the ordering or the halt. It
  would have saved the measured 2.4 ns.
- **String-named events.** Rejected in §D2: unchecked in every direction, and
  its failure mode is silence.
- **A `Handler` interface instead of a func port.** Rejected: it forces an
  adapter type at every call site and, worse, it is a published interface that
  can grow a method — the breakage ADR 0039 exists to prevent. A func type
  cannot grow one at all.
- **`Listener func(ctx, any) (stop bool, err error)`.** Rejected: it puts the
  veto in every listener's signature, so every listener that has no opinion
  must write `return false, nil`, and the authority becomes invisible again —
  it would be in every signature and therefore in none of the wiring.
- **Building on `kernel/topic`.** Rejected in §D9, with the reason recorded
  rather than left as an absence.
- **An `OnDispatch` observation hook on the bus**, in the shape of
  `lifecycle.Config.OnTransition`. Rejected: `Publish` already RETURNS the
  report, so the hook would be a second way to learn the same thing, and it
  would put a nil check on the hot path to serve it.
- **An `Emit[E]` publish helper in `pkg/v1`.** Rejected: `bus.Publish(ctx, ev)`
  is already typed at the call site. Only *registration* needs a type parameter,
  because that is the one place the type is not otherwise present; a second
  spelling of publish buys nothing and costs every reader a choice.
- **A configurable "stop on first error" mode.** Rejected as the §D1 mistake in
  miniature: it is a contract, not a setting, and a bus whose error semantics
  are a constructor argument cannot be reasoned about from a call site.

## Why not dispatch asynchronously "just as an option"

It deserves its own heading because it is the one that will be proposed.

A `Config.Async: true` would be one field and about six lines. It would also
mean that, for those callers, `Publish` returns before the work has happened,
the listener's error has nowhere to go, the publisher's transaction has already
committed, and a process exit loses the work with no record. Every one of those
is a property the caller was relying on this package for, and none of them is
visible at the `Publish` call site — only at a constructor somewhere else.

The SDK already refuses this class of hazard elsewhere: ADR 0030 refuses a
default that writes to `os.Stdout`, and ADR 0031 refuses a policy that runs
while claiming a protection it does not provide. This is the same shape. The
answer for a caller who needs asynchrony is a queue, and the answer for a
caller who needs both is two statements.

## Deferred

- **The `queue` domain.** Inter-process, asynchronous, durable, with retries and
  a dead-letter path. This ADR's frontier table is its boundary; it is named
  here so the next domain starts from a line rather than from a negotiation.
- **Wildcard or hierarchical subscriptions** ("everything under `order.*`").
  Deferred by name: it needs an event *taxonomy*, and §D2 chose the Go type
  precisely because a taxonomy is a naming scheme nobody can check. If it
  returns, it returns as an explicit interface-implements query over registered
  types, decided on its own merits.
- **Ordered delivery across event TYPES.** Priority orders the listeners of one
  type; it says nothing about two different events. Deferred because the
  cross-type case is a sequencing problem, and `lifecycle` (ADR 0050) is where
  sequencing decisions live.
- **A metrics binding** (dispatch count, listener latency, halt rate). The
  `DispatchValue` carries what such a binding would need; wiring it is the
  caller's, and doing it here would make `events` depend on `metrics` for
  everyone who does not want it.

## References

- `internal/core/events/CLAUDE.md` (the frontier table, the port conventions, the Do-NOT list)
- `internal/service/events/CLAUDE.md` (why `kernel/snapshot` and not `kernel/topic`; why registration is a function)
- `internal/service/events/BENCH.md` (the 2.4 ns assertion measurement, the zero-allocation claim, the boxing cost)
- `pkg/v1/events/README.md` (consumer-facing, generated from the package doc comment — ADR 0008)
- `internal/kernel/topic/CLAUDE.md` (the primitive this domain read and did not use)
- `internal/kernel/group/panic.go` (`PanicValue`, and the goroutine boundary that does not exist here)
- `internal/service/lifecycle/errors.go` (the `errors.Join` verdict-beside-cause shape)
