// Package queue — the record the in-memory broker keeps about one message.
package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// memRecord is one message and everything the in-memory broker keeps about
// it: the payload, its identity, where it sits in the visibility order, and
// the lease it is currently under.
//
// It is held by POINTER throughout, so moving a message between the ready
// list and the in-flight map never copies the payload header and never
// desynchronises two copies of the delivery count.
type memRecord struct {
	enqueuedAt time.Time
	visibleAt  time.Time
	expiresAt  time.Time
	id         string
	receipt    corequeue.ReceiptValue
	payload    []byte
	seq        uint64
	deliveries int
}
