// Package queue is a core/data/queue Broker over a NATS JetStream stream and
// one durable pull consumer (ADR 0054's deferred connector, ADR 0211), in the
// third-party/nats vendor module.
//
// # Mapping
//
// Publish is a JetStream publication the stream acknowledges (PubAck); the
// stream sequence is the message's ID, and the enqueue instant, read from
// Config.Clock, travels in a header. Receive is a Fetch of up to max
// messages that waits for none; the delivery count is the server's
// NumDelivered. Ack is an acknowledgement the server confirms (a double
// acknowledgement); Nack is a NakWithDelay by the retry delay, or, once the
// message has been delivered MaxDeliveries times, a publication on the
// dead-letter subject with its cause followed by Term — two requests, so a
// crash between them dead-letters the message twice and never loses it. The
// consumer has no MaxDeliver of its own: the broker owns the decision to
// dead-letter (ADR 0054), and a message whose leases all lapsed is
// dead-lettered by the Receive that finds it past its budget, its Reason
// LEASE_EXPIRED. Kafka is not this port — it has no per-message
// acknowledgement — and belongs to data/stream.
//
// # Whose clock is the lease
//
// The server redelivers on its own clock once AckWait passes. The broker
// reads Config.Clock before each Fetch, so the deadline it hands out never
// falls after the server's, and the lease is the policy's visibility timeout
// or the consumer's AckWait, whichever is shorter. It refuses an Ack, Nack,
// Extend or Reject at or past that deadline with
// core/data/queue.LeaseExpired before anything is sent, so a consumer that
// lost its lease learns it whatever the server did meanwhile. A receipt is
// good with the broker that issued it, whose table holds the delivery that
// acknowledges it; one another broker minted is
// core/data/queue.UnknownReceipt.
//
// # The stream
//
// Config.CreateStream creates a work-queue stream over Subject and
// DeadLetterSubject, so an acknowledged message leaves it and a dead letter,
// which no consumer reads, stays; and the durable consumer, filtered on
// Subject, its AckWait the policy's visibility timeout. A stream made by
// hand must capture both subjects too: DeadLetters reads the dead-letter
// subject back from the same stream by direct get, without consuming it.
//
// # Capabilities
//
// Waker (a publication or hand-back by this process, and the next deadline
// it knows of), LeaseExtender (InProgress resets AckWait, so a renewal lasts
// the shorter of the asked duration and AckWait), Rejecter, DelayedNacker
// (NakWithDelay), OncePublisher (Nats-Msg-Id; JetStream deduplicates over
// the stream's duplicate window and no other, so the window asked for must
// be that one) and DeadLetterReader. Not Releaser: the server's delivery
// count cannot be given back. Every failed request is the package's
// sentinel, core/data/queue.QueueBackendFailed in its trail.
package queue
