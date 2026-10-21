package queue

import "time"

// onceEnd is one entry in the in-memory broker's heap of PublishOnce windows:
// when a reservation ends and which key it holds.
//
// It carries the end as well as the key so a POPPED entry can be checked
// against the key's current reservation — a key reserved again after its
// window ended leaves the old entry in the heap, and comparing the two ends is
// how the stale one is recognised, as leaseExpiry's is.
type onceEnd struct {
	at  time.Time
	key string
}
