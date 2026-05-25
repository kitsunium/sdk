// Package recycler — CappedRecycler[T] layers a reset-on-Put + cap-discard
// policy on top of the plain Recycler[T] declared in recycler.go.
package recycler

// CappedRecycler wraps a concrete *Recycler[T] and layers a cap-discard policy
// on top: on Put, a value whose reported capacity exceeds maxCap is orphaned
// (never reset, never repooled), otherwise it is reset then repooled.
//
// Discard happens BEFORE reset on purpose: it preserves the codec scratch
// detach contract, where an over-cap buffer's backing bytes have been handed
// to the caller and must not be touched. Reset is NOT a memory wipe — a
// consumer recycling sensitive bytes must zero them itself before Put.
//
// Always used behind a pointer: the embedded *Recycler owns a non-copyable
// sync.Pool.
type CappedRecycler[T any] struct {
	// inner is the composed plain recycler. Concrete (not an interface) so
	// Get/Put stay inlinable on the hot path.
	inner *Recycler[T]
	// reset returns a value to its reusable zero state before it is repooled.
	reset func(T)
	// capOf reports the value's current capacity, compared against maxCap.
	capOf func(T) int
	// maxCap is the cap-discard threshold; values above it are orphaned.
	maxCap int
}

// NewCappedRecycler returns a CappedRecycler[T]. newFn builds fresh values,
// resetFn returns a value to its reusable state, and capOfFn reports a value's
// capacity for the discard decision. All three functions are required and a
// nil one panics. maxCap must be positive: a non-positive threshold would
// orphan every value, silently turning the pool into a never-recycling
// allocator, so it panics rather than degrade.
func NewCappedRecycler[T any](newFn func() T, resetFn func(T), capOfFn func(T) int, maxCap int) *CappedRecycler[T] {
	//: reset is mandatory — reset-on-Put is the whole reason this variant exists.
	if resetFn == nil {
		//: programmer error — fail fast at construction.
		panic("recycler: nil reset function")
	}
	//: capOf is mandatory — the discard decision cannot run without it.
	if capOfFn == nil {
		//: programmer error — fail fast at construction.
		panic("recycler: nil cap function")
	}
	//: a non-positive cap orphans everything → a pool that never recycles.
	if maxCap <= 0 {
		//: refuse the degenerate configuration loudly.
		panic("recycler: max capacity must be positive")
	}
	//: compose the plain recycler concretely; NewRecycler validates newFn.
	return &CappedRecycler[T]{
		inner:  NewRecycler(newFn),
		reset:  resetFn,
		capOf:  capOfFn,
		maxCap: maxCap,
	}
}

// Get borrows a value of type T from the underlying recycler, invoking the
// factory on a cache miss. Callers own it until they hand it back via Put.
func (c *CappedRecycler[T]) Get() T {
	//: delegate to the composed recycler's Get.
	return c.inner.Get()
}

// Put returns v for reuse unless its capacity exceeds maxCap, in which case v
// is orphaned (discard-before-reset: never reset, never repooled). Otherwise v
// is reset then repooled. Callers MUST NOT touch v after Put returns.
func (c *CappedRecycler[T]) Put(v T) {
	//: discard oversized values BEFORE reset — orphan them for the GC.
	if c.capOf(v) > c.maxCap {
		//: over-cap: caller may still alias the bytes, so never reset/repool.
		return
	}
	//: under cap — return the value to its reusable state.
	c.reset(v)
	//: hand the clean value back to the pool for the next Get.
	c.inner.Put(v)
}
