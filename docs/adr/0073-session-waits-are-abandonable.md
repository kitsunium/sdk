# ADR 0073 — the session store's two waits can be abandoned, because a blocking flock cannot

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0045](0045-sdk-session-domain.md) — the file store's locking, not the domain
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (the same syscall, the same call, one domain over), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (`Clock` / `Waiter` / `Timed`), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape changing while v0), [ADR 0031](0031-policy-zero-values-are-never-inert.md)

## Context

Every file-store operation runs under one store-wide exclusive lock, because
`rename(2)` makes the PUBLICATION indivisible while only the lock makes the
read-modify-write cycle indivisible. Two waits stood between a caller and that
section, and neither could be left.

The cross-process half was `flock(LOCK_EX)` — blocking, deliberately, with the
reason written down: "an operation waits its turn rather than failing, because
the alternative is a caller retry loop around a lock that is held for
microseconds". The premise is right and the conclusion does not follow. A
blocking `flock` parks the thread inside a syscall no cancellation can reach:
a request whose client hung up, or whose deadline passed, keeps waiting for a
lock nobody will read the result of, and the goroutine comes back only when
some other process releases it. Under a handler pool that is how one slow
neighbour becomes an unavailable service. `withLock` already checked the
context — once, before the wait, which is precisely where cancellation has not
happened yet.

The in-process half was a `sync.Mutex`, and a goroutine parked in `Lock` cannot
be told its caller has gone. Making only the flock abandonable would have moved
the queue one line up.

ADR 0052 met the identical syscall and decided the opposite way: the `lock`
domain never calls a blocking `flock`, polls on an injected clock, and takes a
context-aware gate first. Two packages in one SDK gave opposite answers to the
same question, and the one whose callers are HTTP requests gave the worse one.

## Decision

Both waits observe the caller's context.

- **`flock` is `LOCK_NB` plus a poll** on the injected clock, exactly as
  `internal/service/lock` does. `FileConfig.Poll` is the interval, defaulting to
  `DefaultPoll` — 25 ms, deliberately the same number and the same default as
  the `lock` domain's, so a reader of both does not have to wonder which lock
  they are looking at. Zero is filled, negative is refused: ADR 0031's two
  halves, in one field.
- **The in-process gate is a one-slot channel**, selected against the context.
  It stays FIRST, the same order `lock`'s `nameGate` uses and for the reason
  ADR 0052 measured: flock on the same open file description is a lock
  conversion, so the descriptor this store holds for its lifetime excludes no
  goroutine at all.
- **`FileConfig.Clock` widens from `clock.Clock` to `clock.Timed`**, because
  the store now both stamps and waits — the call `internal/service/sql` already
  made for the same reason. It is a published shape changing while the module
  is v0, said out loud (ADR 0040); `clock.System` and `clock.ManualClock` both
  satisfy `Timed`, so only a hand-written `Clock`-only double is affected.

And the context is checked ONE more time, after both waits and before the
critical section. Each wait is a select, and a select whose cancellation becomes
ready at the same instant as the acquisition picks between them at random — so
without it a caller that had already gone could still have its record read, its
idle window slid and its record republished, under a lock everyone else is
waiting for. The check makes the rule below true in the case where it was a coin
toss.

A caller who leaves receives `STORE_UNAVAILABLE` carrying its own context error
as a field — the retryable backend fault every other unavailability here uses,
rather than a new code, because "this store could not serve you" is what
happened and the cause is already in the fields.

## Consequences

- A cancelled or expired request stops waiting for the lock and for the gate.
  Nothing else about the section changes: it is still one lock, still held for
  microseconds, still released on every path including a panic.
- A contended operation now costs up to one poll interval of latency where it
  previously woke the instant the lock was free. 25 ms is the price, and it is
  the same one the `lock` domain has been paying.
- Contention is TESTABLE for the first time: the poll runs on the injected
  clock, so a `ManualClock` drives a contended store without sleeping.

## Breaking changes

`FileConfig.Clock` is now `clock.Timed`. `clock.System` and `ManualClock`
satisfy it unchanged; a hand-written double implementing only `Now`/`Since`
does not. Permitted only because the module is v0 (ADR 0040), and stated here
rather than discovered.

## Why not

- **Keep the blocking flock and check the context afterwards.** There is no
  afterwards: the goroutine is inside the syscall until some other process
  releases the lock.
- **Keep the `sync.Mutex` and make only the flock cancellable.** The queue moves
  from the kernel to the mutex and nothing observable improves.
- **A shared lock for `Load`.** `Load` writes — it slides the idle window — so a
  shared lock would be the lost update this lock exists to prevent.
- **Per-record locks, so contention disappears.** ADR 0045 already answered
  that: a lock file that is ever unlinked has the well-known two-holder race,
  and never unlinking one leaks a file per session.
- **A new error code for "you left".** `STORE_UNAVAILABLE` is what it is, and
  the caller's own `context.Canceled` is in the fields.

## References

- `internal/service/session/fsguard_unix.go` (`tryLockExclusive`),
  `file_ops.go` (`withLock`, `enter`, `takeFlock`, `waitPoll`),
  `file_config.go` (`Poll`, `DefaultPoll`, `waiter`).
- `TestACallerWhoLeftStopsWaitingForTheLock`,
  `TestTheLockIsTakenOnceTheHolderLeaves`,
  `TestAGoroutineWaitingOnTheGateCanLeaveToo`, and
  `TestACallerThatLeavesWhileAcquiringDoesNotRunTheSection`, whose context
  double reports itself live once and cancelled afterwards — the one instant
  the race lands on, and not otherwise reachable from outside.
- ADR 0052 §"`flock(2)` was measured before use" — the eight-goroutine
  measurement this store's gate exists for.
