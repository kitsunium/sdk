package forward

import "time"

// StatsValue is what a forwarder holds, has done and has lost, read at once
// by [Forwarder.Stats]. The counters count since New; Pending and
// PendingBytes start from the backlog New found in the queue.
type StatsValue struct {
	// LastRetryAfterAt is when a collector last imposed a delay; zero when
	// none has.
	LastRetryAfterAt time.Time
	// PendingBytes is the bytes of the batches the forwarder holds.
	PendingBytes int64
	// LastRetryAfter is the last delay a collector imposed — a Retry-After
	// — as the failure carried it.
	LastRetryAfter time.Duration
	// Pending is the batches the forwarder holds: queued, waiting or being
	// sent.
	Pending int
	// Enqueued counts the batches Enqueue kept.
	Enqueued uint64
	// Sent counts the batches the sender accepted.
	Sent uint64
	// Retries counts the attempts made after a retryable failure.
	Retries uint64
	// Dropped counts the batches lost to a bound: refused by a full queue or
	// as too large, or taken out of it by DropOldest.
	Dropped uint64
	// Expired counts the batches dropped because they outlived MaxAge.
	Expired uint64
	// DeadLettered counts the batches the sender refused for good, kept in
	// the broker's dead-letter store.
	DeadLettered uint64
}
