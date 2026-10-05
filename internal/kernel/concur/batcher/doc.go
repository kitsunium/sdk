// Package batcher provides a generic coalescing buffer: items are appended via
// Add and handed to a deliver closure in batches — flushed eagerly when an
// item-count or weight cap is reached, on an optional background ticker, or on
// an explicit Flush / Close.
//
// The primitive is stdlib-only and domain-neutral (ADR 0014). It owns its own
// ticker — built on Config.Clock, the wall clock by default — and does not
// depend on any other kernel lifecycle primitive. The deliver closure carries
// every domain-specific concern: a
// pre-delivery reorder, a per-batch key, or a byte-vs-count weight all live in
// the caller's Sink and WeightOf, never in the batcher.
//
// Concurrency: Add, Flush and Close are safe to call from multiple goroutines;
// a mutex guards the pending batch. The batch is swapped out under the lock and
// the deliver closure runs outside it, so a slow delivery never blocks a
// producer. Add returns BatcherClosed once Close has run.
//
// Sink serialization (V6): the deliver closure is invoked under a dedicated
// delivery mutex held only across the call, so two flush paths (a cap-triggered
// Add racing the FlushEvery ticker, or two cap-triggered Adds) never enter the
// Sink concurrently. A Sink may therefore assume serial invocation — it can
// append to a shared slice or write to one connection without its own locking.
// Cross-batch ordering is still NOT guaranteed: serialization bounds concurrency
// but not the arrival order of the racing batches. The delivery mutex is
// separate from the pending-batch mutex, so a slow Sink never blocks a producer
// from appending into the next batch.
//
// Package batcher — range 0.1.5.* (ADR 0014 kernel/concur/batcher block).
//
// Package batcher — the Config value type, in its own file per the
// one-exported-struct-per-file convention.
//
// Package batcher — declares the sentinels returned by the Batcher operations.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package batcher
