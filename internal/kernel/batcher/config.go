// Package batcher — the Config value type, in its own file per the
// one-exported-struct-per-file convention.
package batcher

import "time"

// Config tunes a Batcher: the item / weight caps that trigger an eager flush,
// the per-item weight function, the background flush interval, and the
// background-error observer. The zero Config is usable — it batches by item
// count only (each item weighs 1, MaxWeight is ignored) with no caps and no
// ticker, so items accumulate until an explicit Flush or Close.
type Config[T any] struct {
	// MaxItems forces a flush once the pending batch reaches this many items;
	// a non-positive value disables the item-count trigger.
	MaxItems int
	// MaxWeight forces a flush once the pending batch's summed WeightOf reaches
	// it; ignored when WeightOf is nil or MaxWeight is non-positive.
	MaxWeight int64
	// WeightOf reports an item's weight (e.g. its byte size). A nil WeightOf
	// means count-only batching: every item weighs 1 and MaxWeight is ignored.
	WeightOf func(item T) int64
	// FlushEvery, when positive, spawns a ticker goroutine that flushes the
	// pending batch on the interval; Close joins it.
	FlushEvery time.Duration
	// OnError observes deliver failures seen by the background ticker. A nil
	// hook degrades to a no-op so the flush paths stay branch-free.
	OnError func(err error)
}
