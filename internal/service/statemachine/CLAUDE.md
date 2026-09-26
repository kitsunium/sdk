# internal/service/statemachine/

## Purpose

The state-machine ENGINE over stored entities (ADR 0120): `MachineSpec` (the
facade's `Definition`) declares states, event transitions (`On`), timers after
a duration in a state (`After`), deadlines an entity carries (`At`), guards on
the entity (`When`) and hooks (`OnEnter`, `OnTransition`); `StateMachine` (the
facade's `Machine`) runs it over the caller's `core/statemachine.Store`, keeps a
record per entity, and fires timers and guards from its own loop, which sleeps
until the next transition due and wakes on a write. Public facade:
`pkg/v1/statemachine`.

Stdlib plus `core/statemachine`, `kernel/{clock,errs,heap}` and
`service/resilience` (the backoff curve). Code range `0.3.88.*`.

## Contents

| File | Role |
|---|---|
| `machine.go` | package doc; `StateMachine`, `NewStateMachine`, the opening reconciliation (`open`, `loadJournal`, `syncJournal`, `admit`); `Census`, `Record`, `Records` |
| `definition.go` | `MachineSpec` + `NewMachineSpec`, `Initial`/`On`/`After`/`At`/`When`/`OnEnter`/`OnTransition`, `Problems`/`States`/`Transitions`/`InitialState`/`Can`; `TransitionValue`, `ChangeValue` |
| `blueprint.go` | the frozen copy a machine runs: indexes by (event, state) and by state; `dueAt`, `evaluate` (the first-declared rule, a panicking guard recovered) |
| `config.go` | `Config`, `FiringValue`, `DefaultMinGap`, `DefaultMaxHistory`; `settings` — the resolved config and the hooks' nil-safe calls, `finish` (the observer's end, its panic recovered apart) |
| `transition.go` | `Start`, `Fire`; the shared path — `transition` → `locked` (flight, `commit`, `settle`, deferred release) → `replan` → `announce`; `landing` (change, `gone`, late failures); the hooks' panic recovery |
| `notify.go` | `Changed`, `Deleted`, `refresh` (under a read flight), `landRead`, `replan` |
| `book.go` | records, census and flights under one mutex, never held across the journal; the journal writes under a gate per key |
| `agenda.go` | the heap with lazy deletion and its rebuild bound, the dirty set, the per-key backoff, the wake token |
| `loop.go` | `Run`, `Step`, `Wake`, `LoopEvent`; `run`/`wait`/`sleep`/`retarget`, `step`/`guarded`/`process`/`look`/`inspect`/`fire`/`outcome` |
| `locks.go` | the per-key lock table (a one-token channel each, abandonable by `acquire`, or `hold` for the journal gates), the reentrancy mark |
| `codes.go` / `errors.go` | `0.3.88.*`, twenty-one codes |

## Why-this-shape

- **Nothing is held across a panic.** The per-key release is deferred and
  idempotent: `transition` releases early — before the OnTransition hooks — and
  the deferred call is then a no-op; a store that panics still releases.
  `TestAPanickingOnEnterHookFailsItsTransitionAndFreesTheEntity` is the
  framework's own regression test for the lock it once left held.
- **Flights, not waits.** A notification that arrives while a transition holds
  the entity — typically the transition's own write, which a store with write
  hooks reports synchronously, INSIDE the transition — is recorded on the
  flight (`book.touch`, `book.forget`) instead of waiting for the lock it would
  never get. The transition re-reads a touched entity before releasing it
  (`settle`) and skips the step of a deleted one. `Changed` takes the lock only
  when no transition's flight holds the entity, which is what keeps its read
  from being overtaken by a transition's record.
- **A recorded read is a flight too.** The loop's read (`look`) and
  `refresh` open a READ flight (`book.read`) before `Store.Get` and land it
  after the record: `Deleted` marks it, `reconcile` then records nothing, and
  the entity leaves the agenda — a delete landing between the store's answer
  and the record never brings the record back
  (`TestADeletionDuringAReadIsNotUndone`). A read flight takes no writes: a
  writer still waits for the lock. `land` is keyed by the flight's pointer, so
  a late landing never ends a later holder's flight.
- **The journal is ordered per key, not under `book.mu`.** `step`,
  `reconcile` and `forget` take the key's gate (`book.gates`, a `keyLocks`
  held with `hold`, which no context abandons) BEFORE `book.mu`, change the
  record under `book.mu`, release it, and call the journal holding the gate
  alone. Two writes of one key reach the journal in the book's order; a slow
  journal holds back one entity; a journal may read the census
  (`TestAJournalWriteHoldsOnlyItsOwnKey`, `TestAJournalMayReadTheMachine`).
  Without a journal no gate is taken.
- **Gone is the store's word, not a code.** `commit` sets `landing.gone` only
  when the store's `Replace` REFUSED the write; the loop then forgets the
  entity as `Deleted` would. A hook's error carrying `ENTITY_MISSING` is a
  failure like any other (`TestAHookErrorCarryingEntityMissingIsAFailure`).
- **Guards run on the loop's goroutine, never the writer's.** A write only
  marks its entity dirty; `process` evaluates it under the entity's lock with
  panics recovered. A state with only delays is not even marked on a write that
  keeps the state (`replan` reads `blueprint.content`).
- **The dirty set is the truth, the wake token a hint.** A token left by a
  write the previous run already served is ignored (`agenda.pending`), which is
  what keeps an idle loop from running once for nothing.
- **One transition per entity per run.** `take` hands out each key once; a key
  fired in a run is looked at again at the NEXT run, so a cycle of guards turns
  once per `MinGap` instead of spinning.
- **Caller code that reports is called with no lock held.** A journal
  failure met inside a transition travels out of the locked section on the
  `landing` and is reported by `transition` after the release; the loop's
  failures travel on a `pass` and are reported by `step` once `process` has
  released the entity. `TestReportMayCallTheMachine` reads the census from
  `Report` in all three places — it deadlocked on the book's mutex before.
- **The loop survives the caller's panics.** A hook or a guard is recovered
  where it runs; a store, journal or observer-start call that panics inside a
  pass is recovered by `guarded` as that entity's `LoopPanicked`, its lock and
  flights released by the deferred calls already on the stack. The observer's
  END is called by `fire` only after `outcome` has recorded the transition on
  the agenda, through `settings.finish`, which recovers its panic apart
  (`call=observe-end`): reported, never a failure, never a retry of what was
  stored (`TestAnObserversEndThatPanicsChangesNothing`). `Start` and `Fire`
  let such a panic through to their caller, lock released.
- **Per-entity backoff.** `agenda.failed` holds back the failing key only;
  `schedule` clamps any instant to its `notBefore`; a success or a new state
  (`restart`) clears it.
- **Lazy deletion, bounded.** `kernel/heap` has no `Fix`; a reschedule pushes
  a fresh entry and the stale one is dropped when it surfaces, the heap rebuilt
  past `2*live + 64` — `TestTheHeapIsRebuiltBeforeStaleEntriesOutgrowItsBound`.
  Sequence numbers never repeat, so a forgotten key's stale entries can never
  match a later plan.
- **Why `StateMachine` and `MachineSpec`.** ktn-linter's role rule cannot see
  the constructor or the interface a generic type with two type parameters
  satisfies (probed: a one-parameter generic with a single-result constructor
  passes, two parameters do not). The facade aliases them as `Machine` and
  `Definition`, the `secret.Policy = PolicySpec` precedent.
- **`Config` by pointer.** It is 128 bytes and the linter's size rule does not
  exempt a generic `Config`; a nil one reads as the zero configuration.

## Do NOT

- Hold an entity's lock while running an OnTransition hook, or evaluate a guard
  outside `process`.
- Take the per-key lock in `Deleted`, or in `Changed` while a flight holds
  the entity: the notification is often made from inside that very flight — a
  hook deleting its own entity, a store reporting the transition's own write.
- Poll. The loop wakes on the heap's top or on a write, and nothing else.

## Verification

```sh
bazel test --config=race //internal/service/statemachine:statemachine_test
cd internal/service && GOWORK=off go test -race ./statemachine/
cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem ./statemachine/   # BENCH.md
```
