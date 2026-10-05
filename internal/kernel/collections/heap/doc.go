// Package heap provides Heap[T], a binary heap ordered by a comparison
// function the caller supplies. It is a kernel primitive: stdlib-only, and
// domain-neutral down to the signatures — one type parameter and one
// comparison, with no Job, no Task and no Priority anywhere in the API.
//
// # Why not container/heap
//
// container/heap exists and cannot be used cleanly here. It is an ALGORITHM
// over an interface the caller must implement: five methods (Len, Less, Swap,
// Push, Pop) on a named slice type, where Push takes `any` and Pop returns
// `any`. Every use site therefore writes the same boilerplate, boxes each
// element on the way in, and type-asserts it on the way out — a conversion the
// compiler cannot check and the reader has to trust. That was the right design
// in 2011; it predates type parameters.
//
// The generic version costs one comparison function at construction and
// nothing at all at the call site.
//
// # Order
//
// cmp follows the cmp.Compare / slices.SortFunc convention: negative when a
// sorts before b, zero when they tie, positive otherwise. The element that
// sorts FIRST is the one at the top, so cmp.Compare[int] gives a MIN-heap and
// a max-heap is the same function with its arguments reversed. Ties pop in an
// unspecified order — a binary heap is not stable, and no amount of care in
// the comparison makes it so.
//
// # Concurrency
//
// A Heap is NOT safe for concurrent use. That is deliberate: a lock inside
// would be paid by every caller, including the many that hold a heap inside a
// structure they already serialise. The caller picks the lock, or does not
// need one.
package heap
