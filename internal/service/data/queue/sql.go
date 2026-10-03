// Package queue — the SQL broker: the queue in one table of the caller's own
// database, every call on the transaction its context carries (ADR 0151).
package queue

import (
	"context"
	stdsql "database/sql"
	"errors"
	"reflect"
	"strings"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// sqlLeaseEntropyBytes is the entropy in a lease's random half. It is the
// proof a receipt holds the lease — the identifier is known to anyone who can
// read the queue — so it is as wide as a message's own entropy.
const sqlLeaseEntropyBytes int = 8

// sqlReceiptFields is how many dot-separated fields a receipt carries: the
// message identifier, the delivery count, the lease's random half.
const sqlReceiptFields int = 3

// sqlBrokerName names this broker in its refusals' fields.
const sqlBrokerName string = "sql"

// sqlQueueKey names one queue for the process's wake table: the database pool
// and the table. Two brokers over the same pool and table are one queue.
type sqlQueueKey struct {
	// pool is the executor a context with no transaction runs on.
	pool coresql.Executor
	// table is the queue's table.
	table string
}

// sqlWakes is the process's table of SQL queues.
var sqlWakes = newWakeTable[sqlQueueKey]()

// sqlBroker is a queue kept in one table of the caller's own database.
//
// # Why the caller's database, and what that buys
//
// A queue in the database the application already writes can hold a message
// in the SAME transaction as the write that caused it: Publish joins the
// transaction its context carries, so the message exists if and only if that
// transaction commits. That is the transactional outbox, and it is the one
// thing neither other broker can offer — a file broker's publication is
// durable at once, whatever the caller's transaction then does.
//
// # What it keeps, and where
//
// Everything is in the table and nothing in this process: which messages are
// queued, leased and dead, each lease's deadline, each message's delivery
// count. So a consumer that is SIGKILLed loses only its lease, as with the
// file broker, and any process over the same database sees the same queue.
// Exclusion between consumers is the engine's row lock — FOR UPDATE SKIP
// LOCKED on PostgreSQL and MySQL — or SQLite's single writer.
type sqlBroker struct {
	// tm opens the broker's own transactions, and savepoints in the caller's.
	tm coresql.Transactor
	// clk stamps every instant the broker writes.
	clk clock.Clock
	// tx says where a statement runs, and holds a wake until a commit.
	tx sqlParts
	// wake is shared by every broker over the same pool and table in this
	// process, as the file broker's is by every broker over one directory.
	wake *wakeSignal
	// table is the queue's table, which every storage failure names.
	table string
	// stmts are the rendered statements.
	stmts sqlStatements
	// own are the options of a transaction the broker opens itself.
	own    coresql.TxOptionsValue
	policy corequeue.PolicyValue
}

// sqlReceipt is what a receipt names: one lease on one message.
type sqlReceipt struct {
	// id is the message identifier.
	id string
	// lease is the lease's random half, hex.
	lease string
	// deliveries is the delivery count the lease was taken at.
	deliveries int
}

// NewSQL returns a broker whose queue is one table of the caller's database,
// created by [SQLMigration]. It sends no statement: everything decidable
// without a database is decided here.
//
// It refuses, at construction rather than at first use, a policy it cannot
// honour (core/data/queue.QueueMisconfigured) and a transactor, dialect or table it
// cannot use ([corequeue.SQLQueueMisconfigured]).
func NewSQL(cfg SQLConfig) (broker corequeue.Broker, err error) {
	parts, invalid := cfg.validate()
	//: refused before anything is built.
	if invalid != nil {
		//: QueueMisconfigured or SQLQueueMisconfigured.
		return nil, invalid
	}
	//: usable.
	return &sqlBroker{
		tm: cfg.Transactor, clk: clockOrSystem(cfg.Clock), tx: parts,
		wake: sqlWakeFor(parts.join, cfg.Table), table: cfg.Table,
		stmts: renderSQLStatements(cfg.Dialect, cfg.Table), own: sqlOwnTxOptions(cfg.Dialect),
		policy: cfg.Policy.Normalized(),
	}, nil
}

// sqlWakeFor returns the signal every broker over join's pool and table shares
// in this process — or one of its own when the pool cannot key a map: a
// joiner of the caller's making whose pool is nil or of an incomparable type.
// Such a broker still wakes its own consumers; only its siblings over the same
// table fall back to the poll.
func sqlWakeFor(join coresql.Joiner, table string) *wakeSignal {
	pool, _ := join.Join(context.Background())
	//: a key a map can hold without panicking.
	if pool == nil || !reflect.TypeOf(pool).Comparable() {
		//: this broker's own.
		return newWakeSignal()
	}
	//: the queue's.
	return sqlWakes.forKey(sqlQueueKey{pool: pool, table: table})
}

// sqlOwnTxOptions are the options of a transaction the broker opens for a
// call of its own, when the caller's context carries none.
//
// READ COMMITTED on MySQL, as docstore's SQL engine asks for its own (ADR
// 0139): InnoDB's default, REPEATABLE READ, takes gap locks a lease's range
// read does not need and two consumers would then wait on. Every other engine
// keeps its default.
func sqlOwnTxOptions(dialect coresql.Dialect) coresql.TxOptionsValue {
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: no gap locks.
		return coresql.TxOptionsValue{Isolation: stdsql.LevelReadCommitted}
	}
	//: the engine's own default.
	return coresql.TxOptionsValue{}
}

// Publish inserts payload into the queue and returns the message minted for
// it.
//
// Inside a transaction its context carries, the insertion is a savepoint of
// that transaction: the message is receivable once the transaction commits,
// and never existed if it rolls back — and a failed insertion undoes itself
// alone, leaving the caller's transaction usable. Outside one, it is one
// statement on the pool, durable when it returns. Either way the consumers of
// this queue in this process are woken once the message is committed, and
// not before: a wake for a message a rollback took back would find nothing.
func (b *sqlBroker) Publish(
	ctx context.Context, payload []byte,
) (message corequeue.MessageValue, err error) {
	//: a cancelled producer gets its own error rather than a message nobody
	//: asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, ctx.Err()
	}
	//: the bound is enforced at the producer, before a byte is sent.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, tooLarge
	}
	entropy, entropyErr := randomHex(idEntropyBytes)
	//: two processes publishing in the same nanosecond have no shared counter
	//: to break the tie with, so the tie is broken by randomness.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing was sent.
		return corequeue.MessageValue{}, backendFailed("rand", "", entropyErr)
	}
	now := b.clk.Now()
	//: an instant an identifier cannot carry would mint a message whose
	//: receipts this broker refuses: nothing is sent.
	if !nameable(now) {
		//: QueueMisconfigured, naming the clock.
		return corequeue.MessageValue{}, clockRefused(now)
	}
	id := messageID(now.UnixNano(), entropy)
	insertErr := b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		//: one row: live, queued, visible now, never delivered. A nil or empty
		//: payload may be stored as NULL, which reads back as empty.
		if _, execErr := ex.ExecContext(txCtx, b.stmts.insert, []byte(id), now.UnixNano(), now.UnixNano(), payload); execErr != nil {
			//: QueueBackendFailed, the driver's text withheld.
			return b.failed("publish", execErr)
		}
		//: inserted.
		return nil
	})
	//: nothing was published.
	if insertErr != nil {
		//: QueueBackendFailed, or the transactor's own verdict.
		return corequeue.MessageValue{}, insertErr
	}
	b.announce(ctx)
	//: accepted. The returned Payload is the CALLER'S slice: they already hold
	//: those bytes, and a copy would serve no reader.
	return corequeue.MessageValue{ID: id, Payload: payload, EnqueuedAt: now}, nil
}

// Ack deletes the leased message. It is the only call that removes one.
func (b *sqlBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	held, err := b.receiptOf(ctx, receipt)
	//: an unknown receipt and a lapsed one are different bugs.
	if err != nil {
		//: UnknownReceipt, or the caller's deadline.
		return err
	}
	now := b.clk.Now().UnixNano()
	//: one statement: the row goes only if the receipt still holds its lease.
	return b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		result, execErr := ex.ExecContext(txCtx, b.stmts.ack, held.args(now)...)
		//: gone, LeaseExpired, or QueueBackendFailed.
		return b.leased(result, execErr, "acknowledge")
	})
}

// Nack hands the message back and reports what the broker decided.
func (b *sqlBroker) Nack(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error,
) (verdict corequeue.NackValue, err error) {
	held, err := b.receiptOf(ctx, receipt)
	//: an unknown receipt and a lapsed one are different bugs.
	if err != nil {
		//: UnknownReceipt, or the caller's deadline.
		return corequeue.NackValue{}, err
	}
	now := b.clk.Now()
	visibleAt := now.Add(retryDelay(b.policy, held.deliveries))
	//: an instant the table cannot hold is refused before anything moves,
	//: and the lease the caller holds stays valid.
	if !nameable(now) || !nameable(visibleAt) {
		//: QueueMisconfigured, naming the clock.
		return corequeue.NackValue{}, clockRefused(now)
	}
	//: the BROKER owns this decision: the count is the row's, and the
	//: statement below refuses a receipt whose count is not.
	if held.deliveries >= b.policy.MaxDeliveries {
		//: abandoned, with its cause.
		if buryErr := b.buryHeld(ctx, held, describeCause(cause), now); buryErr != nil {
			//: LeaseExpired or QueueBackendFailed.
			return corequeue.NackValue{}, buryErr
		}
		//: never delivered again, unless replayed.
		return corequeue.NackValue{Deliveries: held.deliveries, DeadLettered: true}, nil
	}
	retryErr := b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		result, execErr := ex.ExecContext(txCtx, b.stmts.retry, append([]any{visibleAt.UnixNano()}, held.args(now.UnixNano())...)...)
		//: queued again, LeaseExpired, or QueueBackendFailed.
		return b.leased(result, execErr, "hand a message back")
	})
	//: nothing moved.
	if retryErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return corequeue.NackValue{}, retryErr
	}
	b.announce(ctx)
	//: queued again, eligible at VisibleAt.
	return corequeue.NackValue{Deliveries: held.deliveries, VisibleAt: time.Unix(0, visibleAt.UnixNano())}, nil
}

// Extend renews a lease and mints the receipt that replaces it.
//
// A by whose deadline 64-bit Unix nanoseconds cannot carry — one that reaches
// past 2262-04-11 — is refused with QueueMisconfigured before a statement is
// sent, exactly as the file broker refuses the name it could not write.
func (b *sqlBroker) Extend(
	ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration,
) (lease corequeue.LeaseValue, err error) {
	now := b.clk.Now()
	deadline := now.Add(by)
	//: the two readings of a non-positive renewal are opposites, and a
	//: deadline the table cannot hold would strand the message.
	if by <= 0 || !nameable(deadline) {
		//: QueueMisconfigured, naming the argument, as the other brokers do.
		return corequeue.LeaseValue{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "Extend.by"), kerrs.Int64("value_ns", int64(by)))
	}
	held, err := b.receiptOf(ctx, receipt)
	//: a receipt this broker cannot read renews nothing.
	if err != nil {
		//: UnknownReceipt, or the caller's deadline.
		return corequeue.LeaseValue{}, err
	}
	renewed, entropyErr := randomHex(sqlLeaseEntropyBytes)
	//: a renewal mints a new lease, so it needs a new random half.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing moved.
		return corequeue.LeaseValue{}, backendFailed("rand", "", entropyErr)
	}
	extendErr := b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		args := append([]any{deadline.UnixNano(), []byte(renewed)}, held.args(now.UnixNano())...)
		result, execErr := ex.ExecContext(txCtx, b.stmts.extend, args...)
		//: renewed, LeaseExpired, or QueueBackendFailed.
		return b.leased(result, execErr, "extend a lease")
	})
	//: a lapsed lease is NOT renewed, even when nobody has taken the message.
	if extendErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return corequeue.LeaseValue{}, extendErr
	}
	//: a NEW receipt; the old one names nothing from here on.
	return corequeue.LeaseValue{
		Receipt:   sqlReceiptValue(held.id, held.deliveries, renewed),
		ExpiresAt: time.Unix(0, deadline.UnixNano()),
	}, nil
}

// Wake reports what an idle consumer should wait on: the signal the next
// committed Publish, Nack or replay through any broker over this queue in this
// process closes, and how long until the earliest instant the last Receive
// learnt a message becomes receivable on its own.
//
// Like the file broker's, that instant is only as fresh as the last Receive in
// this process, which reads it from the table; it can be stale only in the
// harmless direction, and a publication from another process closes nothing —
// the poll finds it.
func (b *sqlBroker) Wake() corequeue.WakeValue {
	signal, due := b.wake.current()
	var at time.Time
	//: zero is "nothing scheduled", not the epoch.
	if due != 0 {
		at = time.Unix(0, due)
	}
	//: measured on this broker's clock, now.
	return wakeValue(signal, at, b.clk.Now())
}

// run runs a call's statements where they belong, as docstore's SQL engine
// runs a write (ADR 0139 §D5).
//
// Inside the caller's transaction, every call that writes is a savepoint of
// it, however many statements it sends: its failure undoes it alone, and on
// PostgreSQL clears the aborted state a failed statement leaves, so the
// caller's transaction stays usable. Outside one, a call of several statements
// runs in a transaction of the broker's own, and a call of one statement runs
// on the pool, where one statement is atomic by itself.
func (b *sqlBroker) run(ctx context.Context, several bool, work coresql.TxFunc) error {
	ex, inTx := b.tx.join.Join(ctx)
	//: a savepoint takes no options: it has its transaction's.
	if inTx {
		//: a savepoint of the caller's transaction.
		return b.tm.Transact(ctx, coresql.TxOptionsValue{}, work)
	}
	//: one statement needs no transaction to be atomic.
	if !several {
		//: on the pool.
		return work(ctx, ex)
	}
	//: a transaction of the broker's own.
	return b.tm.Transact(ctx, b.own, work)
}

// announce wakes this queue's idle consumers once the transaction ctx carries
// has committed — at once when the call ran on the pool or in a transaction of
// the broker's own, which has.
func (b *sqlBroker) announce(ctx context.Context) {
	//: held until the caller's transaction commits, and dropped with it.
	if b.tx.hold.Defer(ctx, b.wake.fire) {
		//: the transactor fires it.
		return
	}
	//: already committed.
	b.wake.fire()
}

// buryHeld dead-letters the leased message a receipt names, with cause.
func (b *sqlBroker) buryHeld(ctx context.Context, held sqlReceipt, cause causeValue, now time.Time) error {
	//: the failure's instant is written down, so it must be one the table
	//: holds; the lease stays valid when it is not.
	if !nameable(now) {
		//: QueueMisconfigured, naming the clock.
		return clockRefused(now)
	}
	//: one statement: dead only if the receipt still holds its lease.
	return b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		args := append(causeArgs(now.UnixNano(), cause), held.args(now.UnixNano())...)
		result, execErr := ex.ExecContext(txCtx, b.stmts.bury, args...)
		//: dead, LeaseExpired, or QueueBackendFailed.
		return b.leased(result, execErr, "dead-letter a message")
	})
}

// receiptOf reads a receipt, refusing one this broker could not have minted,
// and a caller that has already given up.
func (b *sqlBroker) receiptOf(ctx context.Context, receipt corequeue.ReceiptValue) (sqlReceipt, error) {
	//: a cancelled caller must not be told its work is safely acknowledged.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return sqlReceipt{}, ctx.Err()
	}
	held, ok := parseSQLReceipt(string(receipt))
	//: not a receipt this package writes.
	if !ok {
		//: UnknownReceipt.
		return sqlReceipt{}, kerrs.Wrap(corequeue.UnknownReceipt, kerrs.WrapParams{},
			kerrs.String("broker", sqlBrokerName))
	}
	//: a receipt, whose lease the statement checks.
	return held, nil
}

// leased turns a statement that ends or renews one lease into the call's
// verdict: one row is the lease still held, none is a lease that lapsed — or
// was renewed, reclaimed, dead-lettered or acknowledged by somebody else.
//
// The refusals are distinguished by SHAPE, as the file broker's are: this
// queue is inter-process, so a receipt minted by another process over the
// same table is legitimate, and a well-formed receipt that matches no lease
// is the one that lost it.
func (b *sqlBroker) leased(result stdsql.Result, execErr error, step string) error {
	n, err := rowsAffected(result, execErr)
	//: the statement did not complete, or its count could not be read.
	if err != nil {
		//: QueueBackendFailed, the driver's text withheld.
		return b.failed(step, err)
	}
	//: the lease is gone: lapsed, whether or not anybody has reclaimed it.
	if n == 0 {
		//: LeaseExpired — the message is queued again or dead-lettered.
		return kerrs.Wrap(corequeue.LeaseExpired, kerrs.WrapParams{}, kerrs.String("broker", sqlBrokerName))
	}
	//: still held, and now ended or renewed.
	return nil
}

// clockRefused is QueueMisconfigured for a clock reading this broker cannot
// write down: before 1970, or so late that an instant it computes from it
// passes 2262-04-11, where 64-bit Unix nanoseconds end. The file broker's
// names end at the same instant.
func clockRefused(now time.Time) error {
	//: the clock, and the instant it read — never a payload.
	return kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
		kerrs.String("field", "Clock"), kerrs.String("instant", now.UTC().Format(time.RFC3339Nano)))
}

// failed is QueueBackendFailed for step, with the driver's error beside it and
// its text withheld by service/data/sql's Withheld. The two are JOINED rather than
// wrapped, so the verdict's code stays the origin whatever the driver's chain
// holds.
func (b *sqlBroker) failed(step string, cause error) error {
	//: the verdict names the table and the step; the cause is reachable, and
	//: silent.
	return errors.Join(
		kerrs.Wrap(corequeue.QueueBackendFailed, kerrs.WrapParams{}, kerrs.String("op", step), kerrs.String("table", b.table)),
		svcsql.NewWithheld(cause))
}

// args binds the condition sqlHeldBy renders: the identifier, the lease, the
// delivery count, and the instant the lease must still be ahead of.
func (r sqlReceipt) args(now int64) []any {
	//: in the order the placeholders are numbered.
	return []any{[]byte(r.id), []byte(r.lease), int64(r.deliveries), now}
}

// causeArgs binds a burial's instant and cause: the instant, the reason, the
// wire-safe Public half and the code, each bounded as the file broker bounds
// its record's header.
func causeArgs(now int64, cause causeValue) []any {
	//: in the order the placeholders are numbered.
	return []any{now, []byte(oneLine(cause.reason)), []byte(oneLine(cause.public)), int64(cause.code)}
}

// sqlReceiptValue renders a receipt: the identifier, the delivery count, the
// lease's random half.
func sqlReceiptValue(id string, deliveries int, lease string) corequeue.ReceiptValue {
	//: three fields no one of which holds a dot.
	return corequeue.ReceiptValue(id + "." + padCount(deliveries) + "." + lease)
}

// parseSQLReceipt reads a receipt back, refusing anything sqlReceiptValue could
// not have written — every field at the width and spelling it is rendered in.
func parseSQLReceipt(receipt string) (held sqlReceipt, ok bool) {
	fields := strings.Split(receipt, ".")
	//: exactly three fields.
	if len(fields) != sqlReceiptFields {
		//: not a receipt.
		return sqlReceipt{}, false
	}
	_, _, minted := parseMessageID(fields[0])
	deliveries, counted := parseCount(fields[1])
	//: an identifier this package mints, a count of at least one delivery,
	//: and a lease's random half.
	if !minted || !counted || deliveries < 1 || !isHexOfWidth(fields[2], sqlLeaseEntropyBytes) {
		//: not a receipt.
		return sqlReceipt{}, false
	}
	//: a receipt, whether or not it still holds its lease.
	return sqlReceipt{id: fields[0], deliveries: deliveries, lease: fields[2]}, true
}

// rowsAffected reads a statement's affected-row count, or its failure.
func rowsAffected(result stdsql.Result, execErr error) (int64, error) {
	//: the statement itself failed.
	if execErr != nil {
		//: no count.
		return 0, execErr
	}
	//: the count, or the driver's failure to give one.
	return result.RowsAffected()
}
