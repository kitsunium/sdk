# cache (service)

Concrete cache stores implementing `internal/core/cache` (ADR 0049): a tagged,
stampede-protected memory store over the kernel LRU+TTL primitive, and the
chain that puts one store in front of another.

```go
store, err := cache.NewMemory[Profile](cache.MemoryConfig{
    MaxEntries: 10_000,        // refused if zero — a capacity is the caller's decision
    DefaultTTL: time.Minute,
})

loader, _ := store.(corecache.Loader[Profile])
p, err := loader.Load(ctx, key, fill)   // one fill per key, per process

tagger, _ := store.(corecache.Tagger)
removed, err := tagger.InvalidateTag(ctx, "user:42")
```

Invalidation is O(k) in the entries carrying the tag, not O(N) in the store —
measured at two store sizes in `BENCH.md`.

Stampede protection collapses concurrent misses **within one process**; it does
not coordinate across replicas.

Public facade: `pkg/v1/cache`. See `CLAUDE.md` for the lock discipline, the
four removal paths, and the chain's ordering rules.
