//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/snapshot .

// Package snapshot holds read-mostly shared state copy-on-write: readers Load
// the current value with one atomic load — no lock, no allocation — and
// writers publish a whole new value, serialised so that a read-modify-write
// never loses a concurrent one.
//
//	routes := snapshot.NewValue(&map[string]Handler{})
//
//	// The hot path: one atomic load, no lock.
//	h := (*routes.Load())[path]
//
//	// A writer clones, changes the clone and publishes it, serialised with
//	// every other writer.
//	routes.Update(func(cur *map[string]Handler) *map[string]Handler {
//	    next := maps.Clone(*cur)
//	    next[path] = handler
//	    return &next
//	})
//
// It is the container the SDK's registries — codecs, writers, exporters — are
// built on, published as an alias of that kernel package (ADR 0159 §4).
//
// # When to use it
//
// For a table read on every request and changed rarely: a registry, a routing
// table, a feature-flag map, a configuration reloaded on a signal. A bare
// sync/atomic.Pointer gives the lock-free read too, but leaves the writer to
// choose between a compare-and-swap loop and a lock of its own; forgetting
// both is the bug — two writers clone the same value and the second publish
// drops the first's change. [Value].Update makes the read-modify-write one
// serialised step.
//
// Not for write-heavy state: every write copies the value. A map written as
// often as it is read wants a lock, or sync.Map.
//
// # The rules a caller keeps
//
//   - The pointer Load returns is shared with every other reader and MUST be
//     treated as immutable: change a clone, then publish it.
//   - An Update function receives the current value (nil while empty), returns
//     the next one, and must not call Store, Swap or Update on the same Value,
//     which would deadlock. Returning the current pointer unchanged publishes
//     nothing, which is how an update aborts.
//   - A Value must not be copied after first use: hold it in a struct used
//     behind a pointer, or behind a pointer itself.
package snapshot
