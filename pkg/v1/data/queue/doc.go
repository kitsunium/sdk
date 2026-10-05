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
// Read this before anything else in the package. pkg/v1/app/events and this
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
// and its text — which quotes rows — withheld; it ends [Consume], as a storage
// failure always has, and a database fails transiently where a directory
// rarely does — so run a consumer over it under lifecycle's supervisor, and
// open SQLite with a busy timeout.
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
