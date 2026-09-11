# internal/kernel/group/

## Purpose

Generic **structured concurrency**: `Group` runs N tasks, waits for all of them
at one point, reports the first error — and delivers a panic raised in a child
goroutine to the waiter instead of letting it kill the process. A kernel
primitive (stdlib-only AND generic — `Group` / `Go` / `Wait` / `Collect`, with
no `Job`, `Task` or `Worker` in a signature). Admitted on **SDK rule 1**, the
only admission criterion there is: stdlib-only AND domain-neutral. The
precedent is ADR 0025, which admitted `kernel/cache` on that rule with **zero**
consumers.

Emits **no error codes**: it forwards whatever a task returns and owns no
failure of its own except a programming fault, which panics.

## Contents

| File | Surface |
|---|---|
| `group.go` | `Group` + `New` / `Go` / `Wait`, `Unlimited`, and the run/fail/capture path |
| `panic.go` | `PanicValue` — the panic carried off a task's goroutine into the waiter |
| `collect.go` | `Collect[T]` — the typed fan-out, results in submission order |

## Why a panic is delivered rather than fatal

A goroutine that panics and is not recovered **on its own stack** kills the
process; `recover()` in the parent cannot see it. That is what turns the common
"spawn N, collect errors" helper into a crash whose stack points at a goroutine
the reader has never met.

So the recovery happens inside the task's goroutine, together with
`debug.Stack()` taken *while that goroutine is unwinding*, and `Wait` re-raises
it as a `PanicValue`. The re-raise happens **after** `wg.Wait()` returns, so the
structured guarantee survives the fault: when `Wait` leaves — by return or by
panic — the group owns no running goroutine.

`PanicValue` is deliberately **not an error**, the same position
`kernel/singleflight` takes: converting a fault into a value the caller may
ignore is how a broken invariant becomes a silent wrong answer. It is a separate
type from singleflight's namesake rather than a shared one, because the message
a crash prints must name the primitive that was running the code that failed;
hoisting the two into a fourth kernel package would add a primitive whose entire
content is two fields.

## The limit, and ADR 0031

`New(parent, limit)`:

- **`limit < 1` clamps to 1.** Serial execution is slow but it is execution. The
  clamp follows the SDK's own bulkhead precedent (ADR 0031 §"Why not extend this
  to the other clamps"): a concurrency floor of one still runs the caller's work
  under a meaningful, if minimal, policy, so it is a *floor* rather than a guess
  at intent.
- **"No limit" must be spelled `Unlimited`.** A zero that silently meant "no
  bound" would be a value the caller never chose doing something they never
  asked for; a zero that meant "no task may run" is the deadlock ADR 0031 exists
  to remove from this SDK (`errgroup.SetLimit(0)` parks every `Go` forever).
  Naming the case leaves neither reading available to a typo.
- `Unlimited` is `math.MaxInt` and is a **real capacity**, not a sentinel: the
  semaphore is a `chan struct{}`, whose buffer costs nothing at any size.
  `TestUnlimitedSemaphoreIsAllocatedNotApproximated` is the guard on that.

## What `Wait` guarantees — and what it cannot

`Wait` returns when every task started by `Go` has **RETURNED**. It does not
return when the context is cancelled, because cancellation is a request and Go
has no way to force a goroutine to honour one.

**A task that ignores its context therefore keeps `Wait` blocked for as long as
it runs.** That is the contract, not a defect. The alternative — returning while
goroutines are still live — is exactly the leak structured concurrency exists to
prevent, and it would hand the caller a "finished" group that is still writing
to their memory. Bound such a task from the *inside* (a deadline on the I/O it
performs); a group cannot bound it from the outside without abandoning it, and
Go has no way to abandon a goroutine.

## Conventions

- **The first error wins, and cancels the rest.** Later errors are dropped:
  almost every one is a consequence of the cancellation the first caused, and
  reporting the cascade would bury the cause.
- **The first error is the context's `Cause`.** The group cancels with
  `context.WithCancelCause`, so a sibling reading `context.Cause(ctx)` can tell
  "another task failed, with this" from "the parent went away".
- **A late submission still runs**, with an already-cancelled context. Skipping
  it silently would be the worse half of the trade — a task that never ran and
  never said so.
- **The bound is taken on the submitting goroutine**, before the goroutine
  exists, so N submissions against a limit of L hold L goroutines, not N.
- **`Go` must not run concurrently with `Wait`.** "Have all submissions been
  made" is a question only the submitter can answer.
- `Collect` is a function, not a method, because Go's methods take no type
  parameters of their own: a `Group[T]` would force `Group[struct{}]` on every
  caller who only wants to wait.
- Cross-OS: 100 % portable (`context`, `sync`, `math`, `runtime/debug`).

## Do NOT

- Return early from `Wait` on cancellation. See above — it converts a blocked
  wait into a goroutine leak the caller cannot see.
- Turn `PanicValue` into an `error`. `TestPanicInATaskReachesTheWaiterCarrying
  TheFailingStack` asserts both the type and the originating stack.
- Read a zero limit as "unbounded". `TestNonPositiveLimitRunsSeriallyInsteadOf
  Deadlocking` asserts the observable outcome (the work ran, one task at a
  time), so it survives a change of mechanism.
- Add error codes. A task's error is passed through untouched; the only failure
  the package owns is a panic.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./group/
bazel test --config=race //internal/kernel/group:group_test
```
