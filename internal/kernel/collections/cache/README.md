# cache

Generic concurrency-safe LRU + TTL cache `Cache[K,V]` — a kernel primitive
(stdlib-only, no domain vocabulary; reuses the kernel clock for testable TTL).

```go
c := cache.NewCache[string, int](cache.Config[string, int]{MaxEntries: 1024, DefaultTTL: time.Minute})
c.Set("k", 7)
v, ok := c.Fetch("k") // 7, true (Fetch, not Get — a hit promotes LRU)
```

`UnlockedMap[K,V]` (`NewUnlockedMap`) is the same cache without its lock, for a
caller that already serialises every call (#1038).

Public facade: `pkg/v1/data/cache`. ADR 0027. See `CLAUDE.md`.
