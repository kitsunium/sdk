package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The queue subsystem's contract (ADR 0179 §3, step 4): what kit's core asks
// of the queues its topics, subscriptions, watches, queued commands and
// mailers' outboxes on a database run on. framework/app/queue sets the hook
// and framework/internal/app/queueengine implements it over the SDK's queue; the
// core reads it and imports none of the SDK's queue. The core encodes every
// message itself: a queue carries bytes, and only the core's typed facades
// (TopicService[T], Command[C, R]) know a message's type.

// Queues is the queue subsystem: the brokers the framework's consumers' queues run on
// and the migration of a queue's table on a database; nil until
// framework/app/queue is imported. A product that declares a topic, a
// subscription, a watch, a queued command or a mailer whose outbox is on a
// database without it is refused at the start (ADR 0179 §6).
var Queues atomic.Pointer[QueueDriverConfig]

// QueueDriverConfig is what framework/app/queue plugs into kit: how a
// consumer's queue opens, the migrations that make its table on a database
// and add the column its dead letters' trails are kept in, and how a
// delivery's handler says that no retry can help.
type QueueDriverConfig struct {
	// Open opens one queue, as cfg places it: on a database, in a
	// directory, or in memory.
	Open func(cfg QueueConfig) (Broker, error)
	// TableMigration is the migration that makes the queue table on a
	// database of the dialect named, at version.
	TableMigration func(dialect, table string, version uint64) (MigrationValue, error)
	// TrailMigration is filled by kit gen.
	TrailMigration func(dialect, table string, version uint64) (MigrationValue, error)
	// DoNotRetry is err as a delivery's handler returns it when no retry
	// can help: its queue dead-letters the message at once.
	DoNotRetry func(err error) error
	// NotRetryable reports whether err, a delivery's handler's, says that no
	// retry can help: what DoNotRetry returned, the product's own included.
	NotRetryable func(err error) bool
}

// QueueConfig is what the framework asks of one queue: where it lies, its policy, the
// app's clock, and for a mailer's outbox the sealing of what it keeps.
type QueueConfig struct {
	// Name names the queue's node in a refusal: the consumer's or the
	// mailer's node ID.
	Name string
	// Dir is the directory a file queue lies in; with no Database and no
	// Dir, the queue is in memory.
	Dir string
	// Database keeps the queue, nil when Dir or memory does: a scope the
	// store subsystem's Database opened, whose transaction a publication in
	// it joins.
	Database Database
	// Table is the queue's table on Database.
	Table string
	// Policy is how the queue leases, retries and bounds its messages.
	Policy QueuePolicyValue
	// Clock is the app's clock, which the queue stamps and waits on.
	Clock Clock
	// Seal, when set, seals every payload on its way in — a mail's, in a
	// mailer's outbox on a database —, and Unseal opens one on its way out;
	// MaxPayload bounds a payload before it is sealed.
	Seal func(ctx context.Context, payload []byte) ([]byte, error)
	// Unseal is the payload a box holds, the box as it lies when it does
	// not open.
	Unseal func(ctx context.Context, box []byte) []byte
	// MaxPayload bounds a payload Seal seals.
	MaxPayload int
	// Wrap, when a test sets it, stands in front of the queue the subsystem
	// opens: a func(queue.Broker) queue.Broker of the SDK's queue, which
	// the framework's core does not name. Nil in every app a product builds.
	Wrap any
}

// QueuePolicyValue is how a queue leases, retries and bounds its messages:
// the SDK's queue policy, in the framework's terms.
type QueuePolicyValue struct {
	// VisibilityTimeout is how long a delivery is leased.
	VisibilityTimeout time.Duration
	// RetryDelay is the wait after a message's first failure, doubled after
	// each, up to MaxRetryDelay.
	RetryDelay time.Duration
	// MaxRetryDelay bounds the wait before a retry; zero leaves it unbound.
	MaxRetryDelay time.Duration
	// MaxDeliveries is how often a message is delivered before it is
	// dead-lettered.
	MaxDeliveries int
	// MaxMessageBytes bounds a message; zero is the queue's own bound.
	MaxMessageBytes int
}

// ConsumeConfig is how a queue's consumer runs: its handler, how long it
// waits idle, how many messages it handles at once and takes at a time, how
// it stops, whether it renews a running handler's lease, and when a handler
// asked to be retried.
type ConsumeConfig struct {
	// Handler handles one delivery: nil acknowledges it, an error has it
	// retried or, through DoNotRetry, dead-lettered at once. Every handler
	// the framework runs tolerates a redelivery.
	Handler func(ctx context.Context, d DeliveryValue) error
	// PollInterval bounds an idle wait.
	PollInterval time.Duration
	// Parallelism is how many deliveries are handled at once.
	Parallelism int
	// BatchSize is how many messages the consumer takes at a time.
	BatchSize int
	// DrainTimeout is how long the handler running when ctx ends may still
	// run before its context is cancelled; zero cancels it at once.
	DrainTimeout time.Duration
	// SettleTimeout bounds each outcome sent once ctx ended, and each lease
	// renewal; zero is the queue's own bound.
	SettleTimeout time.Duration
	// AutoExtend renews the lease of the message a handler runs for as long
	// as it runs.
	AutoExtend bool
	// RetryIn reads, from the error a delivery's handler returned, the delay
	// it asked to be retried after: its queue hands the message back with
	// that delay, the delivery counted. Nil, or false, keeps the queue's own
	// retry delay.
	RetryIn func(err error) (delay time.Duration, asked bool)
}

// MessageValue is one message of a queue: its identifier, its payload as
// the framework encoded it, and when it was published.
type MessageValue struct {
	// EnqueuedAt is when the message was published.
	EnqueuedAt time.Time
	// ID identifies the message in its queue.
	ID string
	// Payload is the message as the framework encoded it.
	Payload []byte
}

// DeliveryValue is one delivery of a message: the message, and how many
// times it has been delivered, this one included.
type DeliveryValue struct {
	// Message is what is delivered.
	Message MessageValue
	// Deliveries counts the deliveries, this one included.
	Deliveries int
}

// LiveMessageValue is a message waiting in its queue, as it lies: when it
// is next visible, how many times it was delivered, and whether a consumer
// holds it.
type LiveMessageValue struct {
	// VisibleAt is when the message is next delivered.
	VisibleAt time.Time
	// Message is the message.
	Message MessageValue
	// Deliveries counts its deliveries so far.
	Deliveries int
	// Leased reports whether a consumer holds it.
	Leased bool
}

// DeadLetterValue is a message its queue gave up on: when, why, and how
// many times it was delivered.
type DeadLetterValue struct {
	// FailedAt is when its last attempt failed.
	FailedAt time.Time
	// Reason is the failure's identifier.
	Reason string
	// Cause is the failure's public text, "" for none.
	Cause string
	// Message is the message, unmodified.
	Message MessageValue
	// Trail is filled by kit gen.
	Trail []uint32
	// Code is the failure's error code, 0 for none.
	Code uint32
	// Deliveries counts its deliveries.
	Deliveries int
}
