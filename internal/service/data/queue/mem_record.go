package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
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
	// lapse names the lease's entry in the broker's expiry heap; the zero
	// Handle while the record is not in flight.
	lapse      heap.Handle
	seq        uint64
	deliveries int
	// priority is the level the message is delivered at; it picks the
	// ready list the record waits in.
	priority corequeue.Priority
}
