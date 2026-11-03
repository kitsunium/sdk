package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// leaseExpiry is one entry in the deadline heap: when a lease lapses and
// which lease it is.
//
// The deadline is the order; the receipt finds the record a popped entry
// lapses. Each lease has exactly one entry, named by the record's Handle, so
// an entry is never stale (#1036).
type leaseExpiry struct {
	at      time.Time
	receipt corequeue.ReceiptValue
}
