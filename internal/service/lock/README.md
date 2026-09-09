# lock (service)

Concrete lockers implementing `internal/core/lock` (ADR 0052): an in-process
locker whose leases **expire**, and a file locker whose leases **do not**.

```go
locker, err := lock.NewMemory(lock.MemoryConfig{TTL: 30 * time.Second})
// or, to exclude other processes on this machine:
locker, err := lock.NewFileLocker(lock.FileConfig{Dir: "/var/run/myapp/locks"})

lease, err := locker.Acquire(ctx, "rebuild:index")
defer lease.Release(ctx)
```

A non-positive TTL is **refused at construction** — never read as "expires
immediately", which would grant every `Acquire` while excluding nobody
(ADR 0031). `Release` releases only a lock this holder still holds. `Extend`
renews; a failed `Extend` means another holder is already inside the section.

`Keepalive` renews in the background and **cancels a derived context** when the
lease is lost, because the caller who needs that news is already inside the
section and the only channel that reaches it is its context.

The file locker takes an in-process gate **before** its `flock`: `flock(2)` is
per open file description, so re-locking a shared descriptor is a no-op and
gives zero exclusion between goroutines — measured at 8 of 8 goroutines inside
one section. Where `flock(2)` does not exist the constructor returns
`proc.UnsupportedPlatform` rather than pretending (ADR 0018).

Public facade: `pkg/v1/lock`. See `CLAUDE.md` for the measurements, the
platform matrix, and what this domain does **not** guarantee.
