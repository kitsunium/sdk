//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/queue .

// Package queue is the public facade for the SDK's ASYNCHRONOUS, DURABLE
// message queue: one process publishes work, another process picks it up
// later, and a crash in between loses nothing.
//
//	broker, err := queue.NewFile(queue.FileConfig{
//		Dir: "/var/lib/myapp/jobs",
//		Policy: queue.Policy{
//			VisibilityTimeout: 30 * time.Second, // longer than the slowest handler
//			MaxDeliveries:     5,                // then the dead-letter store
//			RetryDelay:        2 * time.Second,
//		},
//	})
//
//	// …in the producer, possibly in another process entirely:
//	msg, err := broker.Publish(ctx, payload)
//
//	// …in the consumer:
//	err = queue.Consume(ctx, broker, queue.ConsumerConfig{
//		Handler: func(ctx context.Context, d queue.Delivery) error {
//			return process(ctx, d.Message.Payload) // d.Deliveries says if this is a retry
//		},
//		HandlerIsIdempotent: true, // required; see below
//		Parallelism:         4,
//	})
//
// # This is not an event bus, and the difference is the whole design
//
// Read this before anything else in the package. pkg/v1/events and this
// package are described with the same words — publish, handler, message — and
// they guarantee opposite things:
//
//	                | events                     | queue (this package)
//	----------------|----------------------------|---------------------------
//	Scope           | one process                | many processes
//	Timing          | synchronous                | asynchronous
//	Goroutine       | the publisher's            | a consumer's
//	Transaction     | the publisher's            | its own
//	Durability      | none                       | the point of it
//	Retries / DLQ   | none                       | the point of it
//	If the process  | the event never happened   | the message is still
//	dies mid-flight |                            | there
//
// If you want several things to happen right now, on your goroutine, inside
// your transaction, you want events and this package will be slower and
// harder for no benefit: [Broker.Publish] on a durable broker costs a disk
// flush, because that flush IS the guarantee.
//
// # Delivery is AT LEAST ONCE, and you must handle duplicates
//
// Exactly-once delivery does not exist over a transport. What exists is
// at-least-once delivery plus an idempotent consumer, and any library that
// advertises the first is selling you the second with your half left as an
// exercise. This one says so instead — in three places you cannot miss:
//
//   - [Delivery.Deliveries] is a field of every delivery. It counts from 1,
//     and a value above 1 means these bytes have been handled before, by you
//     or by a consumer in another process.
//   - [ConsumerConfig.HandlerIsIdempotent] must be set to true. Its zero
//     value is refused, so a handler cannot acquire the promise by omission,
//     and `grep -rn HandlerIsIdempotent` enumerates every place in your
//     codebase where somebody made it.
//   - A message is removed at ACKNOWLEDGEMENT, never at read. That is what
//     makes redelivery possible, and it is not negotiable: a consumer that
//     dies after doing the work and before acknowledging it has done the work
//     and not said so, and the queue has no way to tell that from a consumer
//     that died before doing anything.
//
// # Ordering is NOT guaranteed, and a single retry is enough to lose it
//
// One consumer, no failures, one durable broker: messages arrive in
// publication order. Change any of the three and they do not.
//
// The one people discover in production is the retry. A message that fails is
// made visible again LATER, so messages published after it are delivered
// before it is retried. There is no per-key ordering here and no ordered
// mode; if two messages must be processed in order, they are one message.
//
// # What a crash actually costs
//
// A consumer that is SIGKILLed mid-handler holds a lease it will never
// release. Nothing is lost: the lease lapses after
// [Policy.VisibilityTimeout] and the next consumer to look — in any process
// on that machine — picks the message up with [Delivery.Deliveries]
// incremented. That timeout is therefore how long a dead consumer's work sits
// idle, and it is the one number worth thinking about when you wire this.
//
// A message whose consumers keep dying is dead-lettered like any other, by
// the same [Policy.MaxDeliveries] count, so a payload that crashes the
// process cannot loop forever taking every consumer with it.
//
// # The dead-letter store keeps the cause
//
// After [Policy.MaxDeliveries] attempts the message is moved to the
// dead-letter store together with why: the failure's reason, its dotted-quad
// code, and its wire-safe Public half. The log-only Private half is NOT kept
// — a dead-letter store is read by whoever is investigating, often not the
// process or even the trust domain that failed — and [Message.ID] is the join
// key back to the log line that has it.
//
// Read them with the [DeadLetterReader] capability, which both brokers here
// implement. Reading does not remove them; evidence that a read consumes is
// evidence the second investigator does not get.
//
//	if reader, ok := broker.(queue.DeadLetterReader); ok {
//		dead, err := reader.DeadLetters(ctx, 100)
//	}
//
// # Zero values are safe or refused, never inert
//
// [Policy.VisibilityTimeout] and [Policy.MaxDeliveries] are REFUSED at zero,
// because each has two natural readings that are opposites and one of each
// pair silently destroys the guarantee — "redeliver instantly" against "never
// redeliver", "unlimited attempts" against "no attempts". Any value the SDK
// invented would be arbitrary, and a lease lifetime belongs to the work being
// protected.
//
// [Policy.RetryDelay] and [Policy.MaxMessageBytes] are CLAMPED, because their
// zeros have one reading each and it is harmless: no extra delay, and
// [DefaultMaxMessageBytes].
//
// Both durations are also bounded from ABOVE by [MaxDeadlineOffset], a
// century, and refused past it — not as a judgement about leases but because
// the durable broker writes every deadline into a file name as Unix
// nanoseconds, which end in 2262. A deadline past that could not be read
// back, and the message it names would never be delivered again. The
// math.MaxInt64 somebody reaches for to mean "never" is 292 years, so it is
// refused rather than stranding the message; an extension that would reach
// past 2262 is refused by both brokers for the same reason.
//
// # Two brokers
//
// [NewFile] is the real one: its state is a directory, it survives the
// process, and two brokers over one directory — in one process or in twenty —
// are one queue.
//
// [NewMemory] is the test double. It gives your own tests the port's full
// semantics with no directory and no flush, which is what makes them fast
// enough to run on every save. It is one process and it holds nothing on a
// device, so a restart is a total loss: using it in production buys the
// asynchrony and throws away the durability, which is the exact confusion the
// frontier table above exists to prevent.
package queue

import (
	"context"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// DefaultMaxMessageBytes is the payload bound applied when
// [Policy.MaxMessageBytes] is left at zero: one mebibyte.
const DefaultMaxMessageBytes int = corequeue.DefaultMaxMessageBytes

// MaxDeadlineOffset is the ceiling on [Policy.VisibilityTimeout] and
// [Policy.RetryDelay]: a century, refused above it rather than clamped,
// because a deadline past 2262 cannot be written into the durable broker's
// file names and read back.
const MaxDeadlineOffset time.Duration = corequeue.MaxDeadlineOffset

// DefaultPollInterval is how long an idle worker waits before asking again
// when [ConsumerConfig.PollInterval] is left at zero.
const DefaultPollInterval time.Duration = svcqueue.DefaultPollInterval

// Broker is the public alias for the queue contract. It is FROZEN at four
// methods; capabilities arrive as siblings ([DeadLetterReader],
// [LeaseExtender]) reached by type assertion.
type Broker = corequeue.Broker

// Handler is the public alias for the function that processes one delivery.
type Handler = corequeue.Handler

// Message is the public alias for one unit of work as the broker minted it.
type Message = corequeue.MessageValue

// Delivery is the public alias for one message handed to one consumer,
// carrying the lease that proves the claim and the count that says whether
// this is a retry.
type Delivery = corequeue.DeliveryValue

// Lease is the public alias for a consumer's exclusive claim on one message.
type Lease = corequeue.LeaseValue

// Receipt is the public alias for the opaque handle to one lease. Do not
// parse it and do not construct one.
type Receipt = corequeue.ReceiptValue

// Nack is the public alias for what [Broker.Nack] decided: retried, or
// dead-lettered.
type Nack = corequeue.NackValue

// Policy is the public alias for the delivery discipline a broker enforces.
type Policy = corequeue.PolicyValue

// DeadLetter is the public alias for one abandoned message and its cause.
type DeadLetter = corequeue.DeadLetterValue

// DeadLetterReader is the public alias for the capability of reading the
// dead-letter store back. Both brokers here implement it.
type DeadLetterReader = corequeue.DeadLetterReader

// LeaseExtender is the public alias for the capability of renewing a lease a
// handler is still working under. Both brokers here implement it.
type LeaseExtender = corequeue.LeaseExtender

// FileConfig is the public alias for [NewFile]'s configuration.
type FileConfig = svcqueue.FileConfig

// MemoryConfig is the public alias for [NewMemory]'s configuration.
type MemoryConfig = svcqueue.MemoryConfig

// ConsumerConfig is the public alias for [Consume]'s configuration.
type ConsumerConfig = svcqueue.ConsumerConfig

var (
	// QueueMisconfigured refuses a Policy field the broker cannot honour.
	QueueMisconfigured = corequeue.QueueMisconfigured
	// MessageTooLarge refuses a payload above the policy's bound, at the
	// producer, where it can still be made smaller.
	MessageTooLarge = corequeue.MessageTooLarge
	// UnknownReceipt refuses an acknowledgement naming a lease this queue
	// never issued.
	UnknownReceipt = corequeue.UnknownReceipt
	// LeaseExpired reports an acknowledgement whose lease had already lapsed.
	// Nothing happened, and the message is elsewhere.
	LeaseExpired = corequeue.LeaseExpired
	// InvalidBatchSize refuses a non-positive batch size.
	InvalidBatchSize = corequeue.InvalidBatchSize
	// QueueBackendFailed wraps a refusal from the durable broker's storage.
	QueueBackendFailed = svcqueue.QueueBackendFailed
	// QueueDirectoryUnusable refuses a queue directory that is missing, is
	// not a directory, or is writable by accounts that must not be able to
	// inject or drain messages.
	QueueDirectoryUnusable = svcqueue.QueueDirectoryUnusable
	// ConsumerMisconfigured refuses a consumer with no Handler, or one whose
	// author has not asserted HandlerIsIdempotent.
	ConsumerMisconfigured = svcqueue.ConsumerMisconfigured
	// HandlerPanicked reports a recovered handler panic. The message was
	// nacked and the consumer kept running.
	//
	// There is deliberately no HandlerFailed beside it: a handler that
	// returns an error has its error recorded in the dead letter unmodified,
	// because a verdict wrapped around it would displace the reason whoever
	// reads that record actually needs.
	HandlerPanicked = svcqueue.HandlerPanicked
)

// NewFile returns the durable broker: its whole state is the directory in
// cfg.Dir, so it survives the process that published into it and two brokers
// over one directory are one queue.
//
// It refuses at construction — never at first use — a policy it cannot
// honour, a directory it cannot use safely, and a platform with no atomic
// replace or no flushable directory handle.
func NewFile(cfg FileConfig) (broker Broker, err error) {
	//: the facade is a delegation; the broker lives in internal/service.
	return svcqueue.NewFile(cfg)
}

// NewMemory returns the in-process test double: the port's full semantics,
// no directory, no flush, and nothing that survives the process.
//
// It refuses the same policies [NewFile] refuses, through the same guard,
// which is what makes it an honest double.
func NewMemory(cfg MemoryConfig) (broker Broker, err error) {
	//: same delegation.
	return svcqueue.NewMemory(cfg)
}

// Consume runs cfg.Handler against broker until ctx is done, on
// cfg.Parallelism goroutines it owns.
//
// It returns nil when ctx ends — a cancelled consumer is a stopped consumer,
// not a failed one — and returns the broker's error when the STORAGE fails. A
// handler's error is never returned: it is nacked, retried, and eventually
// dead-lettered, which is the entire point of the domain.
//
// cfg.HandlerIsIdempotent must be true; see the package documentation.
func Consume(ctx context.Context, broker Broker, cfg ConsumerConfig) error {
	//: the engine lives with the brokers so the pull loop and the refusals
	//: are written in one place.
	return svcqueue.Consume(ctx, broker, cfg)
}
