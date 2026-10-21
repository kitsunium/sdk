package async

import "time"

// DropPolicy selects how a saturated ring buffer behaves on Write.
type DropPolicy uint8

// DropNewest discards the new entry when the ring is full and surfaces
// BufferFull to the caller. The OnDrop callback fires for the dropped entry.
const DropNewest DropPolicy = 0

// DropOldest discards the oldest queued entry to make room for the new one.
// The OnDrop callback fires for the dropped entry; Write returns nil.
const DropOldest DropPolicy = 1

// Block waits for a free slot when the ring is full — the producer is slowed to
// the downstream's pace instead of losing the entry — for at most MaxBlock on
// the configured Clock. Past the bound the entry is dropped, OnDrop fires and
// Write returns BlockTimeout; a cancelled context returns CtxCancelled and Close
// returns Stopped to every waiting Write. It is Vector's `when_full: block`
// with the one difference ADR 0031 requires: the wait is never unbounded.
const Block DropPolicy = 2

// DefaultMaxBlock is how long a Write waits under the Block policy when
// Config.MaxBlock is unset: one second. Zero does not mean "forever" (ADR 0031):
// a logger that blocks without a bound turns a dead downstream into a stalled
// service, and a caller that wants a longer wait says so, or bounds the wait by
// its context.
const DefaultMaxBlock time.Duration = time.Second
