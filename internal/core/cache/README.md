# cache (core)

The cache **domain** contract: `Store[V]` (`Fetch` / `Set` / `Delete`), the
`EntryValue[V]` a caller stores, and the three siblings a store advertises by
type assertion — `EntryFetcher[V]`, `Tagger`, `Loader[V]`.

```go
var store corecache.Store[Profile] // built in internal/service/cache

if tagger, ok := store.(corecache.Tagger); ok {
    removed, err := tagger.InvalidateTag(ctx, "user:42")
}
```

The read verb is `Fetch`, not `Get`: a hit mutates the store's recency order,
so a read is not free. `Store` is frozen at three methods (ADR 0039) — new
capabilities arrive as siblings.

Stampede protection (`Loader.Load`) collapses concurrent misses **within one
process only**; it does not coordinate across replicas.

Concrete stores: `internal/service/cache`. Public facade: `pkg/v1/cache`.
ADR 0049 (amends ADR 0025). See `CLAUDE.md`.
