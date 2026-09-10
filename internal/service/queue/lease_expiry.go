// Package queue — one entry in the in-memory broker's lease-deadline heap.
package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// leaseExpiry is one entry in the deadline heap: when a lease lapses and
// which lease it is.
//
// It carries the deadline as well as the receipt so a POPPED entry can be
// checked against the record's current deadline — an Extend leaves the old
// entry in the heap, and comparing the two is how the stale one is recognised
// without ever having to find and remove it.
type leaseExpiry struct {
	at      time.Time
	receipt corequeue.ReceiptValue
}
