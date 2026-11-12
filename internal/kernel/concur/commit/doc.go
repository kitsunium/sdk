// Package commit provides Group, a group commit: concurrent callers each
// contribute an item, one goroutine runs a flush over all of them, and every
// caller receives that flush's verdict. N callers that each need their item
// durable share one fsync where each would have paid its own.
//
// It is the primitive between two the kernel already has. singleflight
// deduplicates IDENTICAL calls; batcher coalesces items asynchronously — Add
// returns before delivery and the caller never learns the flush's verdict.
// Here every caller blocks until the flush that carried its item has run,
// and gets its error.
//
// A batch starts with its first item and gathers followers for MaxWait on an
// injected clock, or until MaxItems; flushes run one at a time, in the order
// their batches started, and a batch keeps gathering while the one before it
// flushes — with a zero MaxWait, coalescing comes from the flush's own
// duration. The flush runs on a goroutine of its own, with the first
// caller's context values and none of its cancellation: a caller that leaves
// gets its ctx.Err(), and its item is flushed all the same. A caller whose
// context can never end, on an idle group with no MaxWait, flushes its item
// alone on its own goroutine instead: nobody shares that flush and nobody
// can leave it, and the callers arriving meanwhile gather into the next
// batch, which flushes after it (#813).
//
// Stdlib-only and generic: one type parameter, one function. Range 0.1.4.*
// (ADR 0005 kernel block, #803).
//
// Package commit — one batch: its items, the channels that order and publish
// it, and the verdict.
//
// Package commit — the Config value type.
//
// Package commit — declares the sentinel returned by Do after Close.
package commit
