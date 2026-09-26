# ADR 0120 — a state machine keeps an agenda, not a sweep

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (frozen ports), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (whose type a value is), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock port), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the backoff curve), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code ranges)

## Context

A downstream framework runs state machines over the entities of its document
store: the states an order or a task goes through, the events code fires, the
timers that move an entity after a duration in a state, the deadlines an
entity carries, the guards that move it as soon as a condition on it holds, the
hooks that run on the way, and a history per entity. About 900 lines, and the
mechanism is general — a program that never imports the framework wants it
unchanged. Reading it found six defects:

- **Every wake re-read the whole store.** The machine's loop slept until the
  next transition due and was woken by every write of the store — and then
  listed and decoded EVERY entity to find what was due. A wake cost O(N), and
  so did every write, which is why the loop paced itself at one run a second.
  Measured on the population of this ADR's benchmark: 0.5 ms at a thousand
  entities, 68 ms and 16 MB of garbage at a hundred thousand, per wake.
- **A panicking hook locked the machine for good.** An OnEnter hook ran under
  the machine's mutex; a panic unwound past the unlock and every later
  transition blocked. The framework fixed it (its CodeWorkflowHookPanic); the
  shape invited it again.
- **One mutex serialised every transition** of the machine, whatever the
  entity, and a hook that fired its own machine deadlocked — documented, not
  prevented.
- **A state changed behind the machine's back never timed out.** The loop
  took an entity's entry instant from its record only when the record's state
  matched the entity's; otherwise it used NOW — at every sweep, since nothing
  updated the record. An After timer on such an entity was due "a duration
  from now", forever.
- **The census re-read the store after every transition** to publish counts
  per state: O(N) per transition.
- **One failing entity delayed everybody.** After a run with a failure the
  next run waited out a backoff of 1 s doubling to a minute — for every entity,
  including those whose timers fell due in the meantime.

## Decision

A new domain. The ports and their values in `internal/core/statemachine`, the
engine in `internal/service/statemachine`, the facade `pkg/v1/statemachine`.
Code ranges `0.2.56.*` (`0x00_02_38_*`, one code) and `0.3.88.*`
(`0x00_03_58_*`, twenty-one codes).

### D1 — the caller's store is the source of truth, through a frozen port

The entity's state is a field of the entity, reached through an accessor
`func(*E) *S`, and the entity lives in the caller's store. The engine never
keeps a second copy of it. `Store[E]` is frozen at five methods (ADR 0039):
`Key`, `Get`, `Insert`, `Replace`, `All`. Absence is an ANSWER, not an error:
`Get` reports `found`, `Insert` reports `inserted` (false: the key is taken),
`Replace` reports `replaced` (false: the entity is gone) — so a store never has
to produce a code of this domain to say something ordinary, and an error from
it always means the store failed. `Replace` is the whole of "never resurrect":
a transition whose entity was deleted while it ran stores nothing.

Beside the store the machine keeps what the entity does not carry: one
`RecordValue` per entity — its state, the instant it entered it, its latest
`StepValue`s (`MaxHistory`, 20 by default) — in memory and in an optional
`Journal[S]`, frozen at three methods, whose `Save` and `Delete` take several
records so the opening reconciliation is one call. The engine calls the
journal with no lock of its bookkeeping held, one call at a time PER KEY: a
key's gate is taken before the bookkeeping mutex and held across the call, so
two writes of one key reach the journal in the order the machine made them,
writes of different keys may arrive concurrently, and a slow journal holds
back only the entity it is writing. A journal may read the machine — its
census, its records — and must not tell it anything. Without a journal, a
restart re-enters every entity into its state when the machine opens, which
restarts every After timer; with one, a delay counts from when the entity
really entered its state. `Trigger` names what fired a step — start, event,
delay, deadline, guard — with stable values and `ParseTrigger` for its text.
No registry, as for `lock` and `secret`: a store is a caller's collection.

### D2 — a declaration is data, refused whole

`MachineSpec` (the facade's `Definition`, built by `Define`) declares
`Initial`, `On` (an event a caller fires), `After` (a duration in a state),
`At` (an instant the entity carries), `When` (a guard on the entity, asked each
time the entity is written, never on a clock), `OnEnter` hooks per state and
`OnTransition` hooks. When several automatic transitions of one state are due,
the one declared FIRST fires. A mistake is recorded, not panicked on:
`Problems` reads them back in order, each with log-only fields naming the
event, the state or the function, and `NewStateMachine` refuses them all at
once, with `InitialMissing` and `StoreMissing`. The machine keeps a frozen
copy: a declaration that goes on reaches no machine.

### D3 — one entity, one transition at a time, and nothing held across a panic

Each entity has its own lock — a one-token channel, so a caller whose context
ends stops waiting (`WaitAbandoned`, 503) — handed out by a table that forgets
a key nobody holds or waits for. Transitions of different entities run
concurrently. A transition takes the lock, reads the entity again, sets the new
state, runs the OnEnter hooks of the state entered, checks the hooks changed
neither the state nor the key (`HookChangedState`, `HookChangedKey`), inserts
or replaces, records the step, RELEASES the lock, and only then runs the
OnTransition hooks. The release is deferred and idempotent: a hook's panic is
recovered as `HookPanicked` — the value and the stack as log-only fields, never
the wrap origin — and a panicking store still releases.

An OnEnter hook runs under the lock, so it may not fire its own machine: its
context carries a mark naming the machine's lock table, and `Start` or `Fire`
through it is refused with `Reentrant` instead of deadlocking. An OnTransition
hook runs after the release and may.

Writes and deletes that arrive while a transition holds the entity — very
often the transition's own write, notified by the store — are recorded on the
transition's FLIGHT rather than waiting for the lock: a delete keeps the step
from bringing the record back; a write makes the transition read the entity
again before it lets go, so the record describes what the store holds. A READ
the machine records — the loop's, and `Changed`'s — is a flight too, one that
takes deletions only: a delete that lands between the store's answer and the
record wins, so a read never brings a deleted entity's record back, and a
writer still waits for the lock.

### D4 — the agenda: O(log N) to the next transition due

Each entity in a state that a timer or a guard leaves has ONE entry on a
min-heap (`kernel/heap`): the instant its earliest automatic transition falls
due — a guard that holds is due at once. Rescheduling pushes a fresh entry and
leaves the old one stale, recognised by a sequence number and dropped when it
surfaces; the heap is rebuilt once stale entries outnumber live ones two to
one, so it holds at most 2N + 64 entries. A write — `Changed`, or a transition
stored by `Start` or `Fire` — marks the entity DIRTY; its next due instant is
computed on the loop's goroutine, never on the writer's, because computing it
runs the caller's guards and instant functions. A write that cannot move
anything is not even marked: a state with only delays is not re-evaluated
when a field changes.

Measured (`internal/service/statemachine/BENCH.md`): one due transition,
fired and rescheduled, costs 3.7 µs at a thousand entities and 4.1 µs at a
hundred thousand; the sweep's search alone cost 0.48 ms and 68 ms.

### D5 — the loop sleeps until something is due, and one failure delays one entity

`Run` is the loop: a run takes every dirty entity and every entry due, looks at
each entity once, and fires per entity the first declared transition due; the
entities it fired are looked at again at the next run, which is what bounds a
chain of guards to one step per run. It then sleeps until the heap's top or a
write, never sooner than `MinGap` (1 s by default) after the run ended, so a
burst of writes is one run. The wake is a one-token channel, but the DIRTY SET
is the truth: a token left by a write the last run already served is ignored.
`Step` is one run for a caller that drives the machine itself. One Run or Step
at a time (`LoopRunning`), everything on the caller's goroutine, so a pprof
label the caller sets covers every hook the loop runs.

A transition the loop could not fire — a hook, the store, a guard that
panicked (`FunctionPanicked`), a store or journal call that panicked
(`LoopPanicked`, recovered per entity so the loop goes on) — holds back THAT
entity for `Backoff`
(`resilience.BackoffValue`, 1 s doubling to a minute by default), counted per
entity; the others are not delayed. A success or a new state clears it. An
entity whose write the store REFUSED because it was gone — deleted with no
word to the machine — is not a failure: it is forgotten everywhere, record,
census, journal and agenda, as a read that finds nothing. Only the store's own
refusal reads that way; a hook's error that merely carries `ENTITY_MISSING`
fails the transition like any other.

`Observe` brackets each transition the loop fires — the ones no caller can
wrap — with a context the hooks and the store calls run under; its end hears
the outcome only once the agenda has recorded it, so a panic there is
`LoopPanicked` with `call=observe-end`, reported, and a stored transition is
never retried. `Report` receives every error no return value carries (an
OnTransition hook's, a journal write's, a loop failure) and is called with no
lock of the machine held, so it may read the machine or fire it; `OnLoop` is
told each run and each re-arm.
Nil hooks drop what they would have received — the SDK does not write to stderr
on the caller's behalf (ADR 0030) — and the documentation says to wire
`Report`. The clock is an injected `clock.Timed` (ADR 0090): a `ManualClock`
drives every timer of a test.

### D6 — the store's owner tells the machine about writes it did not make

`Changed(ctx, key)` reads the entity under its lock: a state that changed
behind the machine's back enters the record AS OF NOW — the machine knows the
state, not a transition, so no step is added — and the entity is marked dirty
when a timer or a guard leaves its state. `Deleted(ctx, key)` drops the record,
the census entry and the agenda entry without the lock, since a hook deleting
its own entity holds it. The census is maintained with every record change, so
`Census` costs O(states); every declared state is present.

### D7 — the names the linter accepts

The engine types are `StateMachine` and `MachineSpec`: the linter's role rule
cannot see the constructor or the interface a generic type with two type
parameters satisfies, and the precedent for aliasing a suffixed engine type
under a plain public name is `secret.Policy = PolicySpec`. The facade names
them `Machine` and `Definition`.

## Consequences / Semantics

- The framework rebuilds its Workflow on this engine and keeps what is its own
  — positions for the diagram, spans, the Studio, its wire mapping:
  - a `Store` adapter over its document store: `Key` its key function; `Get`
    its unobserved read (not found → `false, nil`); `Insert` and `Replace` its
    `insertOnly` and `replaceOnly` writes, a Conflict or NotFound mapped to
    `false, nil`; `All` its decode of every entity;
  - its instances file behind a `Journal` (`Save`/`Delete` rewrite the file
    once per call, under a mutex of its own, since writes of different
    entities may now arrive at once; its model.Step maps from StepValue, delay
    and deadline both to "timer", start to "create");
  - its store's `onWrite` and `onDelete` hooks call `Changed` and `Deleted`;
  - `Actor` is its current node, `Observe` its `begin`/`end` span with the
    instance and trigger attributes, `Report` its problem list and log,
    `OnLoop` its loop state (`LoopRunStarted` → begin, `LoopRunEnded` → idle +
    ran, `LoopWaiting` → idle), `MinGap` and `Backoff` its own constants;
  - its `CodeWorkflowHookPanic` becomes an `errs.HasCode(err,
    statemachine.CodeHookPanicked)`, and `TRANSITION_REFUSED` (409) and
    `ENTITY_MISSING` (404) map to its `conflict` and `not_found` wire codes;
  - `Census` and the instance list come from the machine: no store re-read per
    transition.
- Every behaviour the framework's tests pin is kept: the refusals and their
  statuses, OnEnter changing the entity, a panicking OnEnter failing its
  transition with the machine free, a panicking OnTransition leaving the
  transition standing and the next hooks running, a transition never
  resurrecting what its own hook deleted, a guard firing on a write, a
  deadline and a delay firing when due, the loop asleep with nothing due.
- A store written by others must say so, or the machine learns of a write only
  when the entity next falls due or at the next opening.
- A write that does not go through the machine can race with a transition of
  the same entity: the transition's Replace wins. The store is the arbiter;
  nothing here compares versions.

## Breaking changes

None. `statemachine` is a new domain in this change set.

## Alternatives considered

- **Keep the sweep, make it cheaper.** Any loop that re-reads the store to
  find what is due is O(N) per wake, and a write wakes it; a smaller constant
  moves the wall, it does not remove it.
- **A timer per entity.** N runtime timers and a goroutine or a callback each;
  the heap is one timer, rearmed, and its order is exactly the loop's.
- **An indexed heap instead of lazy deletion.** It needs `Fix`/`Remove` in
  `kernel/heap`, a primitive every other caller would pay for; lazy deletion
  with a bounded rebuild keeps the kernel as it is and stays O(log N)
  amortised.
- **One mutex, as before.** Simpler, and a slow hook of one entity blocks every
  other; per-entity locks cost a map entry per entity in flight.
- **Journal writes under the bookkeeping mutex.** It orders every write for
  free, and brings the one mutex back through the side door: a slow journal
  would hold every entity's transition, and a journal or a `Report` that reads
  the census would deadlock on it. A gate per key orders what needs ordering —
  the writes of one entity — and nothing else.
- **Evaluate guards on the writer's goroutine.** The writer is often a request
  handler; a guard is the caller's code and may panic or be slow. The loop is
  where caller code already runs, recovered.
- **A durable workflow runtime** — replay, activities, compensation. That is a
  product, and a different one: this domain drives states of entities the
  caller already stores.

## Deferred

- **Versioned replace.** A `Store` sibling (ADR 0039) with a compare-and-swap
  `Replace` would close the race with writes that bypass the machine.
- **A file journal in the SDK.** The port is small enough for a caller to
  implement over what it already persists; an SDK one would want an append-only
  log with compaction to stay O(1) per step.
- **Processing due entities in parallel** within one run; today a run is
  sequential, which keeps hooks ordered and the pace honest.

## Verification

- `internal/service/statemachine/transition_external_test.go` — events and
  refusals with their statuses, the OnEnter change kept, both hook panics
  (`TestAPanickingOnEnterHookFailsItsTransitionAndFreesTheEntity`), a hook's own
  error kept reachable, the state and key checks, `Reentrant` and a Fire from
  OnTransition, `TestATransitionNeverResurrects`, a delete during the flight,
  store failures, `WaitAbandoned`, and many entities raced from many goroutines.
- `internal/service/statemachine/loop_external_test.go` —
  `TestTheLoopFiresGuardsOnAWriteAndTimersWhenDue` is the framework's own
  workflow test on the engine; the first-declared rule; the per-entity backoff;
  a state changed behind the machine's back; the journal across a restart; a
  deadline brought forward waking the loop; a burst of writes as one run.
- `internal/service/statemachine/edge_external_test.go` — what is due at
  opening, a run stopped half-way, a store failing under the loop, a state
  flipped during a flight, a notification given up on; a journal write that
  holds only its own key and orders a delete after it
  (`TestAJournalWriteHoldsOnlyItsOwnKey`), a journal and a `Report` that read
  the machine, an observer's end that panics, an entity gone under the loop's
  write, a deletion landing during a read (`TestADeletionDuringAReadIsNotUndone`),
  and a hook error carrying `ENTITY_MISSING` treated as a failure.
- `internal/service/statemachine/agenda_internal_test.go` — lazy deletion, the
  2N + 64 bound, the order of a run, the backoff.
- `internal/service/statemachine/agenda_bench_test.go` + `BENCH.md` — the
  O(log N) claim against the sweep's O(N).

## References

- [`container/heap`](https://pkg.go.dev/container/heap) — why the kernel's generic heap is used instead ([`internal/kernel/heap`](../../internal/kernel/heap/CLAUDE.md)).
- [Go Code Review Comments — goroutine lifetimes](https://go.dev/wiki/CodeReviewComments#goroutine-lifetimes) — why Run runs on the caller's goroutine.
