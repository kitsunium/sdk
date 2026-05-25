# snapshot

Copy-on-write container primitive for the SDK kernel. Stdlib-only.

```go
import (
	"maps"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// Read-mostly shared state: lock-free Load, mutex-serialised writers.
var routes snapshot.Value[map[string]int]

// Publish a new snapshot under the writer lock (copy-on-write).
routes.Update(func(cur *map[string]int) *map[string]int {
	next := map[string]int{}
	if cur != nil {
		maps.Copy(next, *cur)
	}
	next["/health"] = 200
	return &next
})

// Read without locking — one atomic load, zero allocation.
if m := routes.Load(); m != nil {
	_ = (*m)["/health"]
}
```

`Value[T]` holds a `*T` behind an `atomic.Pointer[T]`: `Load` is lock-free and
allocation-free, while `Store` / `Swap` / `Update` serialise on a mutex so a
read-modify-write publish is race-free. The codec registry
(`internal/core/codec`) is built on it — three `atomic.Pointer[map]` fields with
hand-rolled CAS loops collapse to three `Value[map]` fields.

See ADR 0011 for the design rationale and the layering contract.
