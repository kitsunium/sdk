// Package sema provides Weighted, a weighted semaphore: a capacity of units,
// Acquire takes n of them, Release gives them back. Where a unit semaphore —
// a buffered channel, a group's limit, a bulkhead — counts calls, this one
// counts what each call costs: the bytes of a read buffer, the size of a
// decompression, the weight of a batch.
//
// Acquire is context-aware and first-in first-out: a waiting request is
// served before every later one, so a large request is never starved by the
// small ones behind it. A request heavier than the whole capacity could
// never be served and is refused at once with WeightTooLarge rather than
// parked forever (ADR 0031). Releasing more than is held panics: a
// programmer's fault, as recycler treats one.
//
// A mutex, a counter and an intrusive list of waiters, each with a channel:
// stdlib-only and generic, nothing allocated on the uncontended path. Range
// 0.1.7.* (ADR 0005 kernel block, ADR 0216).
//
// Package sema — declares the sentinel returned by Acquire.
package sema
