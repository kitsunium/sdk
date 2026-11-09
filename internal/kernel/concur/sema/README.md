# sema

A weighted, context-aware, first-in first-out semaphore (#812, ADR 0216).

```go
budget := sema.NewWeighted(64 << 20) // 64 MiB of decompression in flight
if err := budget.Acquire(ctx, int64(size)); err != nil {
    return err // WeightTooLarge at once, or ctx's error
}
defer budget.Release(int64(size))
```

- `Acquire(ctx, n)` waits behind every earlier `Acquire`: a large request is
  never starved by smaller ones.
- `n` above the capacity is refused with `WeightTooLarge`, never parked.
- `TryAcquire(n)` never waits and never overtakes a waiter.
- `Release` of more than is held panics.
