// Package snapshot provides Value[T any], a copy-on-write container for
// read-mostly shared state. Load is lock-free and allocation-free (a single
// atomic.Pointer load); writers (Store / Swap / Update) serialise on a mutex
// so a read-modify-write publish never loses a concurrent update. Stdlib-only
// and domain-neutral: any frozen-after-init or rarely-mutated table — a codec
// registry, a routing table, a feature-flag map, a hot-reloaded config — can
// reuse it.
//
// The codec registry (internal/core/codec) is built ON this primitive: three
// atomic.Pointer[map] fields with hand-rolled CAS-loop writers collapse to
// three Value[map] fields. The mechanism lives here; the domain clone logic
// stays with the consumer (ADR 0011).
package snapshot

import (
	"sync"
	"sync/atomic"
)

// Value is a copy-on-write container for a *T. Readers call Load with no lock;
// writers replace the whole pointer under a mutex so concurrent
// read-modify-write publishes cannot lose updates. The zero value is ready to
// use — Load returns nil until the first Store. Always used behind a pointer:
// the embedded mutex and atomic.Pointer must not be copied.
type Value[T any] struct {
	// mu serialises writers so Update's read-modify-write is atomic against
	// other writers. Readers (Load) never take it.
	mu sync.Mutex
	// p holds the current value pointer; Load reads it without the lock.
	p atomic.Pointer[T]
}

// New returns a Value holding initial. A nil initial is valid and leaves the
// container empty (Load returns nil), the same state as the zero value —
// offered as call-site sugar when an initial value is already known.
func New[T any](initial *T) *Value[T] {
	//: build the empty container; the zero value is already usable.
	v := &Value[T]{}
	//: a nil initial is a legitimate empty snapshot, not a programmer error.
	if initial != nil {
		//: seed the container so the first Load observes initial.
		v.p.Store(initial)
	}
	//: hand the container back to the caller.
	return v
}

// Load returns the current value pointer without taking the lock — a single
// atomic load, zero allocation. This is the hot path. The returned *T is
// shared with every other reader and MUST be treated as immutable: mutate a
// clone and publish it via Store or Update instead.
func (v *Value[T]) Load() *T {
	//: lock-free read — the entire point of the copy-on-write container.
	return v.p.Load()
}

// Store atomically replaces the current value with next. It takes the writer
// lock so a Store cannot interleave with an in-flight Update's
// read-modify-write and clobber it.
func (v *Value[T]) Store(next *T) {
	//: serialise against other writers (Update / Swap) before publishing.
	v.mu.Lock()
	//: defer the unlock so a panic in any future guarded step still releases it
	//: (matches Swap / Update; keeps the writer lock panic-safe).
	defer v.mu.Unlock()
	//: publish atomically — concurrent Loads observe either old or next, never torn.
	v.p.Store(next)
}

// Swap atomically replaces the current value with next and returns the
// previous value pointer (nil when the container was empty). Takes the writer
// lock for the same reason as Store.
func (v *Value[T]) Swap(next *T) *T {
	//: serialise against other writers before the swap.
	v.mu.Lock()
	//: release the writer lock once the swap has published next.
	defer v.mu.Unlock()
	//: atomic swap hands back the pointer we are displacing.
	return v.p.Swap(next)
}

// Update runs fn under the writer lock and publishes its result. fn receives
// the current value (nil when empty) and returns the replacement; it MUST NOT
// mutate the value it is handed — other goroutines may be reading it — and
// should build and return a fresh *T. fn MUST NOT call Store / Swap / Update
// on the same Value, which would deadlock. Returning the unchanged current
// pointer is a no-op publish, which lets fn abort a conditional update.
func (v *Value[T]) Update(fn func(current *T) (next *T)) {
	//: serialise the read-modify-write so concurrent writers cannot lose updates.
	v.mu.Lock()
	//: release the writer lock after publishing fn's result.
	defer v.mu.Unlock()
	//: read under the lock, transform, and publish the result atomically.
	v.p.Store(fn(v.p.Load()))
}
