// Package queue — hosts the three values that travel between a producer, a
// broker and a consumer: the message, the lease, and the delivery that pairs
// them.
package queue

import "time"

// MessageValue is one unit of work as the broker minted it. Every field is
// assigned by [Broker.Publish]; a caller never fills one in, which is why
// Publish takes a payload rather than one of these.
type MessageValue struct {
	// EnqueuedAt is when the broker accepted the message. It is the ordering
	// key a FIFO implementation sorts on, and it is the broker's clock, not
	// the producer's.
	EnqueuedAt time.Time
	// ID identifies the message for the life of the queue and beyond it: a
	// dead letter carries the same value, so the log line the failing
	// consumer wrote and the record left on disk can be joined without either
	// one carrying the other's half of the story (CLAUDE.md rule 4).
	//
	// It is opaque. Its shape is the implementation's, it is unique within
	// one queue, and nothing outside the broker that minted it may parse it.
	ID string
	// Payload is the caller's bytes, unmodified and uninterpreted. This
	// domain does not encode: pick a codec, marshal, publish the bytes.
	//
	// The slice belongs to the receiver — a durable broker read it from a
	// device, and an in-memory one copies before handing it over, so a
	// handler may keep it, mutate it, or hold it past the acknowledgement
	// without corrupting a redelivery.
	Payload []byte
}

// ReceiptValue is the handle to ONE lease: the only thing [Broker.Ack],
// [Broker.Nack] and [LeaseExtender.Extend] accept, and the only proof that
// this consumer is the one currently holding the message.
//
// It is opaque and it is NOT the message identifier. Two deliveries of the
// same message carry the same [MessageValue.ID] and different receipts, which
// is what lets a broker refuse an acknowledgement from a consumer whose lease
// has already lapsed and been taken by somebody else. Do not parse it, do not
// construct one, and do not store one past the lease it names.
//
// It is a defined string rather than a struct with an unexported field
// because a receipt has to survive being handed to a goroutine, put in a map
// and printed in a log, and because it is not a secret: it names a lease on a
// queue the holder can already read. What it must not be is FORGEABLE into
// somebody else's lease, and that is a property of how the implementation
// mints it, not of the type.
type ReceiptValue string

// LeaseValue is a consumer's exclusive claim on one message: the receipt that
// proves it and the instant it lapses.
//
// The two travel together because neither is usable alone. A receipt without
// a deadline cannot tell a long-running handler whether it still owns the
// work; a deadline without a receipt cannot acknowledge anything.
type LeaseValue struct {
	// ExpiresAt is when the claim lapses and the message becomes eligible for
	// redelivery. It is the broker's clock.
	//
	// It is a promise about VISIBILITY and not about mutual exclusion: after
	// this instant another consumer may hold the same message while this one
	// is still running. A handler that is still working past it is producing
	// a duplicate, which the at-least-once guarantee already permits — see
	// [LeaseExtender] for the way to avoid producing one on purpose.
	ExpiresAt time.Time
	// Receipt names this lease to [Broker.Ack] and [Broker.Nack].
	Receipt ReceiptValue
}

// DeliveryValue is one message handed to one consumer, with everything the
// consumer needs to decide what to do about it.
type DeliveryValue struct {
	// Lease is the claim this delivery grants and the receipt that ends it.
	Lease LeaseValue
	// Message is the work.
	Message MessageValue
	// Deliveries counts how many times this message has been handed to a
	// consumer, INCLUDING this one. It is 1 on the first delivery.
	//
	// It is a field of the port rather than a note in the documentation
	// because it is the at-least-once guarantee made unignorable: a consumer
	// cannot read a delivery without reading the number that says this may
	// not be the first time. A value above 1 means a previous consumer
	// failed, timed out, or died — the three are indistinguishable from
	// here, deliberately, since none of them changes what the handler must
	// do.
	//
	// It is also the number [PolicyValue.MaxDeliveries] is compared against:
	// a delivery whose count already equals the maximum is the last one this
	// message gets before the dead-letter store.
	Deliveries int
}
