// Package queue — the SQL broker's read path: leasing what is due, and
// burying the leases whose consumers died with no attempt left.
package queue

import (
	"context"
	stdsql "database/sql"
	"errors"
	"slices"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// sqlPicked is one row the lease's read returned.
type sqlPicked struct {
	// id is the message identifier.
	id string
	// payload is the message.
	payload []byte
	// enqueuedAt is when the message was first published, Unix nanoseconds.
	enqueuedAt int64
	// deliveries is how many times the message has been handed out so far.
	deliveries int64
	// leased reports a row that was under a lease — now lapsed.
	leased bool
}

// sqlLeaseRound is what one read of due rows decided.
type sqlLeaseRound struct {
	// batch are the deliveries it leased, oldest due first.
	batch []corequeue.DeliveryValue
	// read is how many rows the read returned: fewer than asked means nothing
	// else is due, or everything else due is held by another transaction.
	read int
}

// Receive leases up to max due messages, burying on the way every lapsed
// lease with no attempt left.
//
// An idle Receive is ONE read and takes no lock: the earliest instant any live
// row is due. Only when something is due does it open a transaction — of its
// own, or a savepoint of the caller's — read the due rows in order, locked,
// skipping those another consumer holds, and lease them in one statement. So a
// queue whose consumers sleep on the Waker costs its database one indexed read
// per poll, and none of SQLite's single write lock.
//
// A lease whose consumer died is due at its deadline, and the same read finds
// it: with attempts left it is leased again, its count moved on; without, it
// is dead-lettered with LEASE_EXPIRED, exactly as the other brokers' reclaim
// does. There is no sweeper, here either: a lapsed lease is noticed by
// whoever looks next.
func (b *sqlBroker) Receive(
	ctx context.Context, max int,
) (batch []corequeue.DeliveryValue, err error) {
	//: a consumer asking for nothing would spin forever and process nothing.
	if invalid := checkBatch(max); invalid != nil {
		//: InvalidBatchSize.
		return nil, invalid
	}
	//: a cancelled consumer must not take a lease it cannot serve.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return nil, ctx.Err()
	}
	now := b.clk.Now().UnixNano()
	ex, inTx := b.tx.join.Join(ctx)
	//: outside a transaction, the probe decides whether there is anything to
	//: lease at all. Inside one it is skipped: on MySQL a plain read answers
	//: from the transaction's snapshot, where the locking read does not.
	if !inTx {
		due, probeErr := b.minDue(ctx, ex, b.stmts.probe, "probe the queue")
		//: the database refused.
		if probeErr != nil {
			//: QueueBackendFailed.
			return nil, probeErr
		}
		//: nothing live, or nothing due yet: the record is the wake.
		if due == 0 || due > now {
			b.wake.record(due)
			//: an empty queue is not an error.
			return nil, nil
		}
	}
	var next int64
	leaseErr := b.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		var err error
		batch, next, err = b.leaseDue(txCtx, ex, now, max)
		//: nil, or QueueBackendFailed.
		return err
	})
	//: nothing was leased: the transaction rolled back.
	if leaseErr != nil {
		//: QueueBackendFailed, or the transactor's own verdict.
		return nil, leaseErr
	}
	//: recorded once the leases stand.
	b.wake.record(next)
	//: whatever was due, oldest first.
	return batch, nil
}

// leaseDue leases up to max due rows inside the transaction ex runs, and
// returns them with the instant an idle consumer should look again.
func (b *sqlBroker) leaseDue(
	ctx context.Context, ex coresql.Executor, now int64, max int,
) (batch []corequeue.DeliveryValue, next int64, err error) {
	//: SQLite: the write lock before the first read.
	if b.stmts.lock != "" {
		//: a write that writes nothing.
		if _, lockErr := ex.ExecContext(ctx, b.stmts.lock); lockErr != nil {
			//: QueueBackendFailed.
			return nil, 0, b.failed("lock the queue", lockErr)
		}
	}
	//: until the batch is full, or the read found nothing more.
	for len(batch) < max {
		want := max - len(batch)
		round, roundErr := b.leaseRound(ctx, ex, now, want)
		//: the database refused.
		if roundErr != nil {
			//: QueueBackendFailed.
			return nil, 0, roundErr
		}
		batch = append(batch, round.batch...)
		//: fewer rows than asked for: nothing else due is free to take.
		if round.read < want {
			break
		}
	}
	//: a full batch may have more behind it: look again at once.
	if len(batch) == max {
		//: now.
		return batch, now, nil
	}
	next, err = b.minDue(ctx, ex, b.stmts.next, "read the next due instant", now)
	//: the next instant anything becomes due on its own, zero for none.
	return batch, next, err
}

// leaseRound reads up to want due rows, buries the lapsed ones with no
// attempt left, and leases the rest.
func (b *sqlBroker) leaseRound(ctx context.Context, ex coresql.Executor, now int64, want int) (sqlLeaseRound, error) {
	picked, err := b.pickDue(ctx, ex, now, want)
	//: the database refused.
	if err != nil {
		//: QueueBackendFailed.
		return sqlLeaseRound{}, err
	}
	var buried, leasing []sqlPicked
	//: a lapsed lease with no attempt left is dead; every other row is leased.
	for _, row := range picked {
		//: the signature of a handler that takes its process down with it.
		if row.leased && row.deliveries >= int64(b.policy.MaxDeliveries) {
			buried = append(buried, row)
			continue
		}
		leasing = append(leasing, row)
	}
	//: dead with LEASE_EXPIRED: nobody ever reported a cause.
	if buryErr := b.buryLapsed(ctx, ex, now, buried); buryErr != nil {
		//: QueueBackendFailed.
		return sqlLeaseRound{}, buryErr
	}
	batch, leaseErr := b.leaseRows(ctx, ex, now, leasing)
	//: every row read, leased or buried.
	return sqlLeaseRound{batch: batch, read: len(picked)}, leaseErr
}

// pickDue reads up to want due rows, oldest due first — locked, and skipping
// those another transaction holds, where the engine can.
func (b *sqlBroker) pickDue(ctx context.Context, ex coresql.Executor, now int64, want int) (picked []sqlPicked, err error) {
	rows, err := ex.QueryContext(ctx, b.stmts.pick, now, int64(want))
	//: the read did not start.
	if err != nil {
		//: QueueBackendFailed.
		return nil, b.failed("read the due messages", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = b.finishRows(rows, "read the due messages", err) }()
	//: one message per row.
	for rows.Next() {
		var row sqlPicked
		var lease []byte
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&row.id, &row.enqueuedAt, &row.deliveries, &lease, &row.payload); scanErr != nil {
			//: QueueBackendFailed.
			return nil, b.failed("read the due messages", scanErr)
		}
		row.leased = lease != nil
		picked = append(picked, row)
	}
	//: the finisher reports a failure that ended the rows early.
	return picked, nil
}

// buryLapsed dead-letters the given lapsed leases with LEASE_EXPIRED, a
// bounded number of rows per statement.
func (b *sqlBroker) buryLapsed(ctx context.Context, ex coresql.Executor, now int64, rows []sqlPicked) error {
	//: the same instant and cause for every one of them.
	for chunk := range slices.Chunk(rows, sqlRowsPerStatement) {
		args := append(causeArgs(now, causeValue{reason: reasonLeaseExpired}), idArgs(chunk)...)
		//: dead, and never delivered again.
		if _, err := ex.ExecContext(ctx, b.stmts.buryRows(len(chunk)), args...); err != nil {
			//: QueueBackendFailed.
			return b.failed("dead-letter lapsed leases", err)
		}
	}
	//: buried, or nothing to bury.
	return nil
}

// leaseRows leases the given rows under one lease — one deadline, one random
// half — a bounded number per statement, and returns their deliveries in the
// order they were read.
func (b *sqlBroker) leaseRows(
	ctx context.Context, ex coresql.Executor, now int64, rows []sqlPicked,
) (batch []corequeue.DeliveryValue, err error) {
	//: nothing to lease is no lease.
	if len(rows) == 0 {
		//: nil, so an empty round allocates nothing.
		return nil, nil
	}
	lease, entropyErr := randomHex(sqlLeaseEntropyBytes)
	//: an entropy source that fails is not worked around.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing moved.
		return nil, backendFailed("rand", "", entropyErr)
	}
	deadline := now + int64(b.policy.VisibilityTimeout)
	//: a bounded number of identifiers per statement.
	for chunk := range slices.Chunk(rows, sqlRowsPerStatement) {
		args := append([]any{deadline, []byte(lease)}, idArgs(chunk)...)
		//: leased, the count moved on.
		if _, execErr := ex.ExecContext(ctx, b.stmts.leaseRows(len(chunk)), args...); execErr != nil {
			//: QueueBackendFailed.
			return nil, b.failed("lease the due messages", execErr)
		}
	}
	batch = make([]corequeue.DeliveryValue, 0, len(rows))
	//: the deliveries, with the count that says whether this is the first time.
	for _, row := range rows {
		deliveries := int(row.deliveries) + 1
		batch = append(batch, corequeue.DeliveryValue{
			Message: corequeue.MessageValue{ID: row.id, Payload: row.payload, EnqueuedAt: time.Unix(0, row.enqueuedAt)},
			Lease: corequeue.LeaseValue{
				Receipt: sqlReceiptValue(row.id, deliveries, lease), ExpiresAt: time.Unix(0, deadline),
			},
			Deliveries: deliveries,
		})
	}
	//: oldest due first.
	return batch, nil
}

// minDue runs a query answering one MIN(due) and returns it, zero for NULL —
// no row to take the minimum of.
func (b *sqlBroker) minDue(ctx context.Context, ex coresql.Executor, query, step string, args ...any) (int64, error) {
	var due stdsql.NullInt64
	//: one aggregate row, always.
	if err := ex.QueryRowContext(ctx, query, args...).Scan(&due); err != nil {
		//: QueueBackendFailed.
		return 0, b.failed(step, err)
	}
	//: zero when NULL, which is "nothing".
	return due.Int64, nil
}

// rowSet is what finishing a read needs of *sql.Rows: the failure that ended
// the iteration early, and the close.
type rowSet interface {
	// Err returns the failure that ended the iteration, or nil.
	Err() error
	// Close releases the rows.
	Close() error
}

// finishRows closes rows and returns the read's verdict: err when the read had
// already failed, else the failure that ended the iteration early — which Next
// reports only as "no more rows" — else the close's.
func (b *sqlBroker) finishRows(rows rowSet, step string, err error) error {
	iterErr, closeErr := rows.Err(), rows.Close()
	//: the first failure wins.
	if err != nil {
		//: already failed.
		return err
	}
	//: the iteration's own failure, or the close's.
	if cause := errors.Join(iterErr, closeErr); cause != nil {
		//: QueueBackendFailed.
		return b.failed(step, cause)
	}
	//: every row read.
	return nil
}

// idArgs binds the identifiers of rows.
func idArgs(rows []sqlPicked) []any {
	args := make([]any, len(rows))
	//: in the order the placeholders are numbered.
	for i, row := range rows {
		args[i] = []byte(row.id)
	}
	//: one per row.
	return args
}
