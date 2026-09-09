# internal/core/lock/

## Purpose

The SDK's mutual-exclusion **domain**: the `Locker` port that hands out named
exclusive leases, the `Lease` a holder gets back, and the `Deadliner` sibling
through which a lease says whether it can expire at all. The 17th core sibling,
admitted by **ADR 0052**. The concrete lockers — in-process and file-backed —
live in `internal/service/lock`.

Code range: `0.2.21.*` (ADR 0052).

## Why this shape

**A lock that lies is worse than no lock.** A caller who believes they hold a
critical section behaves differently from one who knows they do not. A lock
reporting success it cannot back removes the second behaviour and leaves
nothing in its place, so the failure is silent, unreproducible, and discovered
as corrupted data rather than as an error. Every decision below follows.

**Expiring a lease does not stop its old holder.** A stalls, its lease lapses,
B acquires, A resumes with no idea anything happened and writes into B's
section. The lock is not holding A — A's own *belief* is. The domain decides
all three consequences rather than leaving them emergent:

| Decision | Shape | Refusal |
|---|---|---|
| **Ownership** | `Lease` carries a holder token; `Release` releases ONLY a lock this holder still holds | a superseded lease releases **nothing** and reports `LOCK_NOT_HELD` |
| **Renewal** | `Lease.Extend` renews a full lifetime from now | `Extend` on a lapsed lease is refused **even when nobody has taken the lock**, because expiry is the deadline, not "until someone else takes it" |
| **Fencing** | `Lease.Fence() uint64`, strictly increasing per name across every acquisition | never 0 for a live lease, so a forgotten plumb-through cannot compare equal to a real token |

**What is NOT guaranteed, stated here because it must not be discovered:**
issuing a fencing token is not enforcing one. The SDK hands the caller a
number; only the **resource** can compare it and refuse the lower one, and many
cannot (a plain file, an HTTP endpoint, a table with no version column). Where
the fence is not checked, **mutual exclusion is not guaranteed against a
stalled holder** — a GC pause, a `SIGSTOP` or a descheduled thread can put two
holders inside one section and neither will notice. If the resource cannot
check a fence, do not rely on a TTL for correctness: use the file locker, whose
leases do not expire.

**No TTL on the port.** A lease lifetime belongs to the work being protected,
which is known where a `Locker` is wired, not at the call site. A per-call
duration would create exactly one place where a zero can be typed by accident,
in a hot path, and be read as a lock that already expired. The TTL lives in the
service constructor's configuration and is refused there (ADR 0031).

## Surface

| Symbol | Notes |
|---|---|
| `Locker` | `Acquire` / `TryAcquire`. **FROZEN at two** (ADR 0039) — guarded by `TestTwoMethodDoubleStillSatisfiesLocker` |
| `Lease` | `Fence` / `Extend` / `Release`. **FROZEN at three** — guarded by `TestThreeMethodDoubleStillSatisfiesLease` |
| `Deadliner` | sibling: `Deadline() time.Time`. Implemented ONLY by a lease that can be taken from a live holder |
| `LockMisconfigured` `0.2.21.1` | constructor refusal — most importantly a non-positive TTL |
| `LockNotHeld` `0.2.21.2` | `Extend`/`Release` from a holder that no longer owns the lock |
| `LockBackendFailed` `0.2.21.3` | the locker could not answer. A lock merely being HELD is **not** this |
| `LockNameRejected` `0.2.21.4` | empty or unrepresentable name |

## Conventions

- **`TryAcquire` reporting "held elsewhere" is `(nil, false, nil)`.** A caller
  who offered to give up immediately has had the offer accepted; nothing went
  wrong. An error means the backend could not answer the question at all.
- **A context cancellation returns `ctx.Err()`**, not an SDK sentinel — the
  caller supplied the deadline and already knows what it means. Follows
  `resilience`'s precedent.
- **`Deadliner` is a type assertion, not a zero time.** A zero `time.Time` is a
  value someone will compare against `time.Now` and lose to; an absent
  interface is not.

## Do NOT

- **Add a method to `Locker` or `Lease`.** `pkg/v1/lock` aliases both and Go
  interfaces are structural: a new method breaks every downstream double at
  compile time with no deprecation window. New capabilities are siblings
  (ADR 0039) — `Deadliner` is the model.
- **Fold `Deadline` into `Lease`.** The whole point is that a file lease does
  NOT answer it. `TestDeadlinerIsASiblingAndNotPartOfLease` fails if it does.
- **Add a TTL parameter to `Acquire`.** See §Why this shape.
- **Add a registry.** The two backends differ in whether a lease can be taken
  from a live holder; resolving one from a config string would let a typo swap
  them silently, with every call still succeeding.
- **Weaken the "not guaranteed" paragraph.** It is repeated in four places on
  purpose.

## Verification

```
bazel test --config=race //internal/core/lock:lock_test
# or: cd internal/core && GOWORK=off go test -race ./lock/...
```
