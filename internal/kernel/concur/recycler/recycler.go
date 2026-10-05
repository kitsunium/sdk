package recycler

// NewPool returns a Pool[T] whose factory fires on every cache miss.
// A nil factory is a programmer error and panics at construction rather than
// deferring the panic to sync.Pool.Get on the first cache miss, far from the
// offending call site.
func NewPool[T any](newFn func() T) *Pool[T] {
	//: refuse a nil factory loudly at construction, not on first Get.
	if newFn == nil {
		//: programmer error — fail fast at the call site.
		panic("recycler: nil factory")
	}
	//: build the recycler with a thin closure boxing the factory output for sync.Pool.
	r := &Pool[T]{}
	r.pool.New = func() any {
		//: invoke the caller's factory and hand its result to sync.Pool as any.
		return newFn()
	}
	//: hand the concrete recycler back to the caller.
	return r
}

// Get borrows a value of type T, invoking the factory on a cache miss. The
// returned value MAY be a previously Put value or a freshly built one. The
// comma-ok assertion is fail-loud: the pool only ever holds T (both New and
// Put are typed), so a wrong-typed entry is an internal invariant break that
// panics rather than silently masking it (mirrors core/data/codec/scratch.poolGet).
func (r *Pool[T]) Get() T {
	//: the pool only ever holds T — comma-ok guards the invariant.
	v, ok := r.pool.Get().(T)
	//: a wrong-typed entry is impossible by contract; fail loud if it happens.
	if !ok {
		//: never mask a broken pool invariant behind a zero value.
		panic("recycler: pool yielded unexpected type")
	}
	//: return the recycled or freshly built value.
	return v
}

// Put returns v to the pool for future reuse. Callers MUST NOT touch v after
// Put returns; the pool may hand it to another goroutine immediately.
func (r *Pool[T]) Put(v T) {
	//: hand the value back to sync.Pool — boxing a pointer-sized T is alloc-free.
	r.pool.Put(v)
}
