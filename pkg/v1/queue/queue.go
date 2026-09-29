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
// harder for no benefit: [Broker].Publish on a durable broker costs a disk
// flush, because that flush IS the guarantee.
//
// The Transaction row is about where the WORK runs, and a handler always runs
// in its own. What [NewSQL] adds is that the MESSAGE can be part of yours: see
// "A queue in your own database" below.
//
// # Delivery is AT LEAST ONCE, and you must handle duplicates
//
// Exactly-once delivery does not exist over a transport. What exists is
// at-least-once delivery plus an idempotent consumer, and any library that
// advertises the first is selling you the second with your half left as an
// exercise. This one says so instead — in three places you cannot miss:
//
//   - [Delivery].Deliveries is a field of every delivery. It counts from 1,
//     and a value above 1 means these bytes have been handled before, by you
//     or by a consumer in another process.
//   - [ConsumerConfig].HandlerIsIdempotent must be set to true. Its zero
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
// [Policy].VisibilityTimeout and the next consumer to look — in any process
// on that machine — picks the message up with [Delivery].Deliveries
// incremented. That timeout is therefore how long a dead consumer's work sits
// idle, and it is the one number worth thinking about when you wire this.
//
// A message whose consumers keep dying is dead-lettered like any other, by
// the same [Policy].MaxDeliveries count, so a payload that crashes the
// process cannot loop forever taking every consumer with it.
//
// # The dead-letter store keeps the cause
//
// After [Policy].MaxDeliveries attempts the message is moved to the
// dead-letter store together with why: the failure's reason, its dotted-quad
// code, and its wire-safe Public half. The log-only Private half is NOT kept
// — a dead-letter store is read by whoever is investigating, often not the
// process or even the trust domain that failed — and [Message].ID is the join
// key back to the log line that has it.
//
// Read them with the [DeadLetterReader] capability, which every broker here
// implements. Reading does not remove them; evidence that a read consumes is
// evidence the second investigator does not get.
//
//	if reader, ok := broker.(queue.DeadLetterReader); ok {
//		dead, err := reader.DeadLetters(ctx, 100)
//	}
//
// What becomes of one is then a decision, and [DeadLetterManager] is where it
// is taken: ReplayDeadLetter queues it again — same ID, same payload, its
// count reset, so it has every attempt again — once the downstream is fixed,
// and DeleteDeadLetter removes it for good. An ID the store does not hold is
// [DeadLetterNotFound].
//
//	if manager, ok := broker.(queue.DeadLetterManager); ok {
//		err := manager.ReplayDeadLetter(ctx, dead[0].Message.ID)
//	}
//
// # A failure no retry can fix is dead-lettered at once
//
// A payload that does not decode will not decode on the fifth attempt either.
// Return [DoNotRetry] around the error and [Consume] dead-letters the message
// on its FIRST failure, through the broker's [Rejecter], with your error as
// the cause the dead letter records:
//
//	var order Order
//	if err := json.Unmarshal(d.Message.Payload, &order); err != nil {
//		return queue.DoNotRetry(err)
//	}
//
// Keep it for failures that belong to the message. A downstream that is down,
// a lock that is held, a deadline that passed are what retries are for. The
// mark is recognised with errs.HasCode(err, queue.CodeNotRetryable); a broker
// of your own without [Rejecter] is nacked as before, and the message then
// reaches the dead-letter store after its last attempt with the same cause.
//
// # A retry delay that grows
//
// [Policy].RetryDelay is the same wait after every failure. Set
// [Policy].MaxRetryDelay and it grows instead — RetryDelay after the first
// failure, doubling, never more than the ceiling — so a downstream that is
// down is asked less and less often:
//
//	queue.Policy{
//		VisibilityTimeout: 30 * time.Second,
//		MaxDeliveries:     10,
//		RetryDelay:        time.Second,     // 1 s, 2 s, 4 s, …
//		MaxRetryDelay:     5 * time.Minute, // … never more than five minutes
//	}
//
// Zero keeps the constant delay every policy had before the field existed. A
// ceiling with no RetryDelay to grow from, or one below it, is refused.
//
// # Zero values are safe or refused, never inert
//
// [Policy].VisibilityTimeout and [Policy].MaxDeliveries are REFUSED at zero,
// because each has two natural readings that are opposites and one of each
// pair silently destroys the guarantee — "redeliver instantly" against "never
// redeliver", "unlimited attempts" against "no attempts". Any value the SDK
// invented would be arbitrary, and a lease lifetime belongs to the work being
// protected.
//
// [Policy].RetryDelay and [Policy].MaxMessageBytes are CLAMPED, because their
// zeros have one reading each and it is harmless: no extra delay, and
// [DefaultMaxMessageBytes].
//
// The three durations are also bounded from ABOVE by [MaxDeadlineOffset], a
// century, and refused past it — not as a judgement about leases but because
// the durable brokers write every deadline as Unix nanoseconds, into a file
// name or a 64-bit column, which end in 2262. A deadline past that could not
// be read back, and the message it names would never be delivered again. The
// math.MaxInt64 somebody reaches for to mean "never" is 292 years, so it is
// refused rather than stranding the message; an extension that would reach
// past 2262 is refused by every broker for the same reason.
//
// # An idle consumer sleeps until there is work
//
// Every broker here implements [Waker], and [Consume] waits on it: a Publish,
// a Nack or a replay in this process wakes an idle worker at once, and a retry
// delay ending or a lease lapsing wakes it at that instant. [ConsumerConfig]
// .PollInterval is then an upper bound rather than a cadence — what is left
// for it to find is a message ANOTHER process published into a durable queue —
// so an idle consumer costs nothing between polls and a poll of a few seconds
// loses no latency inside one process:
//
//	queue.Consume(ctx, broker, queue.ConsumerConfig{
//		Handler:             handle,
//		HandlerIsIdempotent: true,
//		PollInterval:        5 * time.Second, // bounds only a publication from another process
//	})
//
// Two durable brokers over one directory — or one database and table — in one
// process share their wake, as they share everything else: they are one queue.
//
// # A queue in your own database, joined to your transaction
//
// [NewSQL] keeps the queue in one table of the PostgreSQL, MySQL or SQLite
// database your application already writes, handed over as the [sql.Transactor]
// you open your own transactions with. Every call runs on the transaction its
// context carries — a savepoint of it — so a message published inside yours
// exists if and only if yours commits. That is the transactional outbox: the
// write and the message that announces it are never one without the other,
// and a process that dies between your commit and a publication loses
// nothing, because there is no between.
//
//	create, err := queue.SQLMigration(sql.DialectPostgres, "app__jobs", 20260930120000) // run by your Migrator
//	jobs, err := queue.NewSQL(queue.SQLConfig{
//		Transactor: tm, Dialect: sql.DialectPostgres, Table: "app__jobs",
//		Policy: queue.Policy{VisibilityTimeout: 30 * time.Second, MaxDeliveries: 5},
//	})
//	err = sql.Transact(ctx, tm, func(ctx context.Context, _ sql.Executor) error {
//		if err := orders.Insert(ctx, order); err != nil {
//			return err // no message either
//		}
//		_, err := jobs.Publish(ctx, payload) // joins this transaction
//		return err
//	})
//
// Idle consumers are woken once the publication's transaction commits, never
// for one rolled back. An idle Receive is one indexed read and takes no lock;
// a Receive that leases locks the rows it takes and skips those another
// consumer holds (FOR UPDATE SKIP LOCKED — MySQL 8.0.1, MariaDB 10.6) or, on
// SQLite, takes the one write lock. A failure of the database is
// [QueueBackendFailed], with the driver's error reachable through errors.As
// and its text — which quotes rows — withheld.
//
// # Three brokers
//
// [NewFile] and [NewSQL] are the real ones. [NewFile]'s state is a directory,
// it survives the process, and two brokers over one directory — in one process
// or in twenty — are one queue. [NewSQL]'s state is a table, with the same
// guarantees, and the one property a directory cannot give: a publication
// inside your database transaction.
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

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// DefaultMaxMessageBytes is the payload bound applied when
// [Policy].MaxMessageBytes is left at zero: one mebibyte.
const DefaultMaxMessageBytes int = corequeue.DefaultMaxMessageBytes

// MaxDeadlineOffset is the ceiling on [Policy].VisibilityTimeout and
// [Policy].RetryDelay: a century, refused above it rather than clamped,
// because a deadline past 2262 cannot be written into the durable broker's
// file names and read back.
const MaxDeadlineOffset time.Duration = corequeue.MaxDeadlineOffset

// DefaultPollInterval is how long an idle worker waits before asking again
// when [ConsumerConfig].PollInterval is left at zero. With a [Waker] broker it
// bounds only how late another process's publication is noticed.
const DefaultPollInterval time.Duration = svcqueue.DefaultPollInterval

// MaxSQLTableLen is the longest table name [NewSQL] and [SQLMigration] accept,
// in bytes: PostgreSQL's identifier limit.
const MaxSQLTableLen int = svcqueue.MaxSQLTableLen

// The codes of the sentinels ADR 0151 added, for errs.HasCode.
const (
	// CodeNotRetryable is the code [DoNotRetry] adds to a failure's wrap
	// trail: errs.HasCode(err, CodeNotRetryable) recognises the mark, where
	// errors.Is(err, NotRetryable) sees only an origin.
	CodeNotRetryable errs.Code = corequeue.CodeNotRetryable
	// CodeDeadLetterNotFound is [DeadLetterNotFound]'s code.
	CodeDeadLetterNotFound errs.Code = corequeue.CodeDeadLetterNotFound
	// CodeSQLQueueMisconfigured is [SQLQueueMisconfigured]'s code.
	CodeSQLQueueMisconfigured errs.Code = svcqueue.CodeSQLQueueMisconfigured
)

// Broker is the public alias for the queue contract. It is FROZEN at four
// methods; capabilities arrive as siblings ([DeadLetterReader],
// [LeaseExtender], [Waker], [Rejecter], [DeadLetterManager]) reached by type
// assertion.
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

// Nack is the public alias for what [Broker].Nack decided: retried, or
// dead-lettered.
type Nack = corequeue.NackValue

// Policy is the public alias for the delivery discipline a broker enforces.
type Policy = corequeue.PolicyValue

// DeadLetter is the public alias for one abandoned message and its cause.
type DeadLetter = corequeue.DeadLetterValue

// DeadLetterReader is the public alias for the capability of reading the
// dead-letter store back. Every broker here implements it.
type DeadLetterReader = corequeue.DeadLetterReader

// LeaseExtender is the public alias for the capability of renewing a lease a
// handler is still working under. Every broker here implements it.
type LeaseExtender = corequeue.LeaseExtender

// Waker is the public alias for the capability of telling an idle consumer
// when to look again. Every broker here implements it, and Consume uses it; a
// broker of your own that omits it is simply polled.
type Waker = corequeue.Waker

// Wake is the public alias for what an idle consumer waits on: a signal the
// next Publish or Nack in this process closes, and how long until something
// the broker holds becomes receivable on its own.
type Wake = corequeue.WakeValue

// Rejecter is the public alias for the capability of dead-lettering a leased
// message at once, with the cause that condemned it. Every broker here
// implements it, and Consume uses it for a [DoNotRetry] failure.
type Rejecter = corequeue.Rejecter

// DeadLetterManager is the public alias for the capability of replaying a
// dead letter into its queue, its count reset, or deleting it. Every broker
// here implements it.
type DeadLetterManager = corequeue.DeadLetterManager

// SQLConfig is the public alias for [NewSQL]'s configuration: Transactor,
// Dialect and Table (required), Policy, and Clock.
type SQLConfig = svcqueue.SQLConfig

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
	// NotRetryable is the failure no retry can fix: what a dead letter
	// records when [DoNotRetry] marked an error with no public words of its
	// own, or none at all.
	NotRetryable = corequeue.NotRetryable
	// DeadLetterNotFound refuses a replay or a deletion naming a dead letter
	// the store does not hold.
	DeadLetterNotFound = corequeue.DeadLetterNotFound
	// SQLQueueMisconfigured refuses an SQLConfig no SQL broker could run: no
	// transactor, one it cannot join a transaction of, a dialect it cannot
	// spell, or a table name it cannot use.
	SQLQueueMisconfigured = svcqueue.SQLQueueMisconfigured
	// QueueBackendFailed reports a refusal from a durable broker's storage:
	// the filesystem's, or a statement the SQL broker's database did not
	// complete.
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
// replace or no flushable directory handle. Windows is such a platform: after
// the directory checks — which read its access control lists there — NewFile
// returns the SDK's UNSUPPORTED_PLATFORM, matched with
// errors.Is(err, proc.UnsupportedPlatform).
//
// The broker holds two directory descriptors and additionally implements
// io.Closer, which releases them — reached by type assertion, as the session
// file store's is, so Broker grows no method:
//
//	if closer, ok := broker.(io.Closer); ok { defer closer.Close() }
//
// The messages stay on disk; every call after Close fails.
func NewFile(cfg FileConfig) (broker Broker, err error) {
	//: the facade is a delegation; the broker lives in internal/service.
	return svcqueue.NewFile(cfg)
}

// NewSQL returns a durable broker whose queue is one table of the caller's
// database, created by [SQLMigration]. Every call runs on the transaction its
// context carries for cfg.Transactor, so a message published inside that
// transaction exists if and only if it commits. It sends no statement.
//
// It refuses at construction — never at first use — a policy it cannot honour
// and a transactor, dialect or table it cannot use ([SQLQueueMisconfigured]).
// The transactor must be the SDK's, or another core/sql Joiner and Deferrer.
func NewSQL(cfg SQLConfig) (broker Broker, err error) {
	//: the facade is a delegation; the broker lives in internal/service.
	return svcqueue.NewSQL(cfg)
}

// SQLMigration returns the migration that creates the one table an SQL queue
// named table keeps on dialect, numbered version for the caller's own version
// table. Its Down drops the table, and every message in it. Its statement does
// nothing when the table exists.
func SQLMigration(dialect sql.Dialect, table string, version uint64) (sql.Migration, error) {
	//: delegate verbatim to the service constructor.
	return svcqueue.SQLMigration(dialect, table, version)
}

// DoNotRetry marks cause as a failure no retry can fix: returned from a
// [Handler], it makes [Consume] dead-letter the message at once, with cause,
// instead of handing it back for another attempt. An SDK error keeps its own
// reason, code and public words in the dead letter; any other error, or none,
// is recorded as [NotRetryable].
func DoNotRetry(cause error) error {
	//: the mark is core's, so a second consumer engine reads it the same way.
	return corequeue.DoNotRetry(cause)
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
