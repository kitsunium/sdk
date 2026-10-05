// Package recycler — CappedPool[T] layers a reset-on-Put + cap-discard
// policy on top of the plain Pool[T] declared in recycler.go.
//
// Package recycler provides Pool[T any], a generic typed object pool
// backed by sync.Pool. Callers recycle ANY pointer-sized object (event
// records, attribute slices, scratch structs) without paying the boxing cost
// on Get/Put. Stdlib-only and domain-neutral: any byte buffer, codec stream,
// HTTP body encoder, or metrics line writer can reuse it.
//
// "Pointer-sized" is load-bearing, not decoration. sync.Pool stores any, and
// only a value that fits in an interface word is boxed for free — so a
// *[]byte costs nothing to Put while a bare []byte, being a three-word
// header, heap-allocates 24 B on every single Put. Measured in BENCH.md at
// 25.70 ns and 0 allocs against 51.36 ns and 1 alloc for the same workload,
// and 3.496 ns against 19.82 ns under b.RunParallel. Pool a pointer. Every
// consumer in this repository already does.
//
// The byte-slice pool (internal/kernel/concur/buffer) and the codec scratch buffer
// pool (internal/core/data/codec/scratch) are built ON this primitive; Pool[T]
// is the shared mechanism, the capacity thresholds stay with the consumers.
package recycler
