// Package sqs is a core/data/queue Broker over Amazon SQS (ADR 0054's deferred
// connector, ADR 0211). It lives in the third-party/aws module beside the s3
// and cloudwatch writers, so it costs a consumer no vendor the module did not
// already carry (ADR 0157).
//
// # Mapping
//
// Publish is SendMessage: the payload base64-encoded in the body, since an
// SQS body is text, and the enqueue instant, read from Config.Clock, carried
// as a message attribute; the MessageId is the message's ID. Receive is one
// ReceiveMessage of at most MaxReceiveBatch messages, its VisibilityTimeout
// the policy's rounded up to the second and its WaitTimeSeconds
// Config.WaitTime: long polling is the wait, so the broker has no Waker.
// Deliveries is ApproximateReceiveCount. Ack is DeleteMessage; Nack is
// ChangeMessageVisibility to the retry delay, or, once the message has been
// delivered MaxDeliveries times, a SendMessage to the dead-letter queue with
// its cause followed by a DeleteMessage — two requests, so a crash between
// them dead-letters the message twice and never loses it. A message whose
// leases all lapsed is dead-lettered by the Receive that finds it past its
// budget, its Reason LEASE_EXPIRED.
//
// # Whose clock is the lease
//
// SQS keeps the visibility timeout on its own clock and to the second, and a
// DeleteMessage with an old receipt handle may succeed. The broker therefore
// reads Config.Clock before each ReceiveMessage, so the deadline it hands out
// never falls after the one SQS keeps, writes it into the receipt, and
// refuses an Ack, Nack, Extend or Reject at or past it with
// core/data/queue.LeaseExpired, LeaseLost in its trail, before anything is
// sent: a consumer that lost its lease learns it, whatever SQS would have
// done. The deadline is only as true as the two clocks agree. A receipt is
// good with the broker that issued it, whose table holds the receipt handle
// and the payload a dead letter needs; one minted by another broker is
// core/data/queue.UnknownReceipt, and a renewal voids the receipt it
// replaces.
//
// # Capabilities
//
// LeaseExtender (ChangeMessageVisibility), Rejecter (dead-lettered at once),
// DelayedNacker (up to MaxVisibility), DelayedPublisher (DelaySeconds, up to
// MaxPublishDelay) and DeadLetterReader (a ReceiveMessage on the dead-letter
// queue with a zero visibility timeout, which hides nothing; SQS samples a
// standard queue, so a listing longer than a page may miss one). Not
// Releaser: SQS cannot give back a receive count. Not OncePublisher: SQS
// deduplicates on FIFO queues only, which ADR 0054 defers with per-key
// ordering. Not Waker: long polling is the wait. Every failed request is the
// package's sentinel, core/data/queue.QueueBackendFailed in its trail.
package sqs
