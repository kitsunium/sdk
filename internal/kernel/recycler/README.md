# recycler

Generic object-recycling primitive for the SDK kernel. Stdlib-only.

```go
import "github.com/kitsunium/sdk/internal/kernel/recycler"

// Plain recycler — no reset; the consumer resets if it needs to.
r := recycler.NewRecycler[*bytes.Buffer](func() *bytes.Buffer { return new(bytes.Buffer) })
b := r.Get()
// ... use b ...
b.Reset()
r.Put(b)
```

`Recycler[T]` recycles any pointer-sized typed object through a `sync.Pool`
without boxing on the call path. `CappedRecycler[T]` (see CLAUDE.md) layers a
reset-on-Put + capacity-discard policy on top, used by `internal/kernel/buffer`
(64 KiB) and `internal/core/codec/scratch` (256 KiB).

See ADR 0010 for the design rationale and the layering contract.
