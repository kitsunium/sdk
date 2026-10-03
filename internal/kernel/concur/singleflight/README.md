# singleflight

Generic call deduplication `Group[K,V]` — a kernel primitive (stdlib-only, no
domain vocabulary). N concurrent callers naming the same key produce exactly
one execution, and all N receive its result.

```go
var group singleflight.Group[string, []byte]

body, shared, err := group.Do(ctx, url, func(callCtx context.Context) ([]byte, error) {
    return fetch(callCtx, url) // runs once, however many callers arrive
})
```

A caller that abandons (its context is cancelled) stops waiting and gets its
own `ctx.Err()`; the shared call keeps running for everyone else, and is
cancelled only when the **last** caller leaves. A panic in the call is
re-raised in every waiter as a `PanicValue` carrying the originating stack.

It is not a cache (nothing is remembered once the call ends) and it
deduplicates **within one process only**.

ADR 0049. See `CLAUDE.md` for the design, and `BENCH.md` for the measured
threshold below which a `Group` costs more than it saves.
