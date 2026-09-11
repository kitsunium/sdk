# lock (core)

The mutual-exclusion **domain** contract: `Locker` (`Acquire` / `TryAcquire`),
the `Lease` it returns (`Fence` / `Extend` / `Release`), and the `Deadliner`
sibling a lease implements only when it CAN expire.

```go
var locker corelock.Locker // built in internal/service/lock

lease, err := locker.Acquire(ctx, "rebuild:index")
defer lease.Release(ctx)

if d, ok := lease.(corelock.Deadliner); ok {
    // this lease can be taken from you at d.Deadline(); renew or fence
}
```

`Locker` is frozen at two methods and `Lease` at three (ADR 0039) — new
capabilities arrive as siblings. Neither takes a TTL: a lease lifetime is
configured where the locker is built, and a non-positive one is refused there
rather than read as "already expired" (ADR 0031).

`Release` releases only a lock this holder **still** holds. A lease that was
taken over after lapsing releases nothing and reports `LOCK_NOT_HELD`, because
unlocking a section another holder is inside is worse than leaking the lock.

`Fence()` is a per-name, strictly increasing token — **but the SDK can only
issue it.** Unless the protected resource compares it and refuses the lower
one, mutual exclusion is not guaranteed against a stalled holder.

Concrete lockers: `internal/service/lock`. Public facade: `pkg/v1/lock`.
ADR 0052. See `CLAUDE.md`.
