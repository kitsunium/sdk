package queue

import (
	"cmp"
	"context"
	stdsql "database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
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
	at, instantsErr := b.receiveInstants(ctx)
	//: a clock this broker cannot write down, or a sweep the database refused.
	if instantsErr != nil {
		//: QueueMisconfigured, or QueueBackendFailed.
		return nil, instantsErr
	}
	ex, inTx := b.tx.join.Join(ctx)
	//: outside a transaction, the probe decides whether there is anything to
	//: lease at all. Inside one it is skipped: on MySQL a plain read answers
	//: from the transaction's snapshot, where the locking read does not.
	if !inTx {
		probed, answered, probeErr := b.probeOutside(ctx, ex, at, max)
		//: nothing due, a lease in one statement, or the database refused.
		if answered {
			//: the batch, empty or not, or QueueBackendFailed.
			return probed, probeErr
		}
	}
	//: the lease in a transaction: the broker's own, or the caller's.
	return b.leaseInTransaction(ctx, at, max)
}

// receiveInstants reads the clock once for a Receive — the instant a lease is
// judged against and the deadline one taken now gets — refusing a clock this
// broker could not write down, and sweeps the dead letters past their
// retention on the same look, at most once a minute, in a statement of its
// own.
func (b *sqlBroker) receiveInstants(ctx context.Context) (sqlInstants, error) {
	clockNow := b.clk.Now()
	deadline := clockNow.Add(b.policy.VisibilityTimeout)
	//: a lease this broker could not write down — a clock outside what 64-bit
	//: Unix nanoseconds carry — is refused before any statement is sent.
	if !nameable(clockNow) || !nameable(deadline) {
		//: QueueMisconfigured, naming the clock.
		return sqlInstants{}, clockRefused(clockNow)
	}
	at := sqlInstants{now: clockNow.UnixNano(), deadline: deadline.UnixNano()}
	//: QueueBackendFailed when the sweep's statement was refused.
	return at, b.sweepRetention(ctx, at.now)
}

// probeOutside asks, outside any transaction, whether anything is due, and
// answers the Receive when it can: nothing due — the probe's record is the
// wake —, or, on PostgreSQL, the lease taken in one statement with no
// transaction of the broker's own (issue #1042). answered is false when the
// lease must run in a transaction.
func (b *sqlBroker) probeOutside(
	ctx context.Context, ex coresql.Executor, at sqlInstants, max int,
) (batch []corequeue.DeliveryValue, answered bool, err error) {
	due, probeErr := b.minDue(ctx, ex, b.stmts.probe, "probe the queue")
	//: the database refused.
	if probeErr != nil {
		//: QueueBackendFailed.
		return nil, true, probeErr
	}
	//: nothing live — NULL, never a zero instant — or nothing due yet:
	//: the record is the wake.
	if !due.Valid || due.Int64 > at.now {
		b.wake.record(due.Int64)
		//: an empty queue is not an error.
		return nil, true, nil
	}
	//: PostgreSQL leases in one statement.
	if b.stmts.leaseReturning != "" {
		batch, err = b.receiveReturning(ctx, ex, at, max)
		//: whatever was due, oldest enqueued first.
		return batch, true, err
	}
	//: the other engines lease in a transaction.
	return nil, false, nil
}

// leaseInTransaction leases up to max due rows in a transaction — the
// broker's own, or a savepoint of the caller's — and records when the next
// row becomes due once the leases stand.
func (b *sqlBroker) leaseInTransaction(ctx context.Context, at sqlInstants, max int) ([]corequeue.DeliveryValue, error) {
	var batch []corequeue.DeliveryValue
	var next int64
	leaseErr := b.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		var err error
		batch, next, err = b.leaseDue(txCtx, ex, at, max)
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

// sqlInstants are the two instants one Receive reads its clock for: the
// instant a lease is judged against, and the deadline a lease taken now gets.
type sqlInstants struct {
	// now is the instant, in Unix nanoseconds.
	now int64
	// deadline is now plus the visibility timeout, checked representable.
	deadline int64
}

// leaseDue leases up to max due rows inside the transaction ex runs, and
// returns them with the instant an idle consumer should look again.
func (b *sqlBroker) leaseDue(
	ctx context.Context, ex coresql.Executor, at sqlInstants, max int,
) (batch []corequeue.DeliveryValue, next int64, err error) {
	//: SQLite: the write lock before the first read.
	if b.stmts.lock != "" {
		//: a write that writes nothing.
		if _, lockErr := ex.ExecContext(ctx, b.stmts.lock); lockErr != nil {
			//: QueueBackendFailed.
			return nil, 0, b.failed("lock the queue", lockErr)
		}
	}
	//: a message past MaxAge is buried before any is picked, so none is
	//: delivered; a policy with no MaxAge sends nothing more.
	if expireErr := b.expireDue(ctx, ex, at.now); expireErr != nil {
		//: QueueBackendFailed.
		return nil, 0, expireErr
	}
	//: until the batch is full, or the read found nothing more.
	for len(batch) < max {
		want := max - len(batch)
		round, roundErr := b.leaseRound(ctx, ex, at, want)
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
		return batch, at.now, nil
	}
	due, err := b.minDue(ctx, ex, b.stmts.next, "read the next due instant", at.now)
	//: the next instant anything becomes due on its own — and, at the
	//: latest, the deadline of a lease taken now: this Receive's own, or one
	//: another consumer was taking on a row the locked read skipped, which the
	//: read cannot see. Early costs one empty Receive; late would cost a poll.
	return batch, earliest(due.Int64, at.deadline), err
}

// leaseRound reads up to want due rows, buries the lapsed ones with no
// attempt left, and leases the rest.
func (b *sqlBroker) leaseRound(ctx context.Context, ex coresql.Executor, at sqlInstants, want int) (sqlLeaseRound, error) {
	picked, err := b.pickDue(ctx, ex, at.now, want)
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
	if buryErr := b.buryLapsed(ctx, ex, at.now, buried); buryErr != nil {
		//: QueueBackendFailed.
		return sqlLeaseRound{}, buryErr
	}
	batch, leaseErr := b.leaseRows(ctx, ex, at.deadline, leasing)
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
		args := append(b.stmts.causeArgs(now, causeValue{reason: reasonLeaseExpired}), idArgs(chunk)...)
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
	ctx context.Context, ex coresql.Executor, deadline int64, rows []sqlPicked,
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

// minDue runs a query answering one MIN(due) and returns it — NULL, which is
// "no row to take the minimum of", kept apart from a real instant of zero.
func (b *sqlBroker) minDue(
	ctx context.Context, ex coresql.Executor, query, step string, args ...any,
) (due stdsql.NullInt64, err error) {
	//: one aggregate row, always.
	if scanErr := ex.QueryRowContext(ctx, query, args...).Scan(&due); scanErr != nil {
		//: QueueBackendFailed.
		return stdsql.NullInt64{}, b.failed(step, scanErr)
	}
	//: an instant, or NULL.
	return due, nil
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

// receiveReturning leases up to max due messages outside the caller's
// transaction in ONE statement — leaseReturning, atomic by itself, so it runs
// on the pool with no BEGIN and no COMMIT (issue #1042). A batch it did not
// fill buries the lapsed leases with no attempt left, which the statement
// skips, and asks when the next message is due. A policy with a MaxAge buries
// the expired messages first, in a statement of its own.
//
// RETURNING promises no order, so the batch is sorted by the instant each row
// was due, then its identifier: pick's order, the queue's visibility order.
func (b *sqlBroker) receiveReturning(
	ctx context.Context, ex coresql.Executor, at sqlInstants, max int,
) (batch []corequeue.DeliveryValue, err error) {
	//: a message past MaxAge is buried before any is leased.
	if expireErr := b.expireDue(ctx, ex, at.now); expireErr != nil {
		//: QueueBackendFailed.
		return nil, expireErr
	}
	batch, err = b.leaseReturning(ctx, ex, at, max)
	//: the database refused.
	if err != nil {
		//: QueueBackendFailed.
		return nil, err
	}
	//: a full batch may have more behind it: look again at once.
	if len(batch) == max {
		b.wake.record(at.now)
		//: the batch.
		return batch, nil
	}
	args := append(b.stmts.causeArgs(at.now, causeValue{reason: reasonLeaseExpired}), at.now, int64(b.policy.MaxDeliveries))
	//: the lapsed leases the lease skipped, which only a burial moves.
	if _, buryErr := ex.ExecContext(ctx, b.stmts.buryExhausted, args...); buryErr != nil {
		//: QueueBackendFailed.
		return nil, b.failed("dead-letter lapsed leases", buryErr)
	}
	due, nextErr := b.minDue(ctx, ex, b.stmts.next, "read the next due instant", at.now)
	//: the database refused.
	if nextErr != nil {
		//: QueueBackendFailed.
		return nil, nextErr
	}
	b.wake.record(earliest(due.Int64, at.deadline))
	//: whatever was due, oldest enqueued first.
	return batch, nil
}

// leaseReturning sends the one statement that leases, and reads the rows it
// leased into deliveries under one lease, in identifier order.
func (b *sqlBroker) leaseReturning(
	ctx context.Context, ex coresql.Executor, at sqlInstants, max int,
) (batch []corequeue.DeliveryValue, err error) {
	lease, entropyErr := randomHex(sqlLeaseEntropyBytes)
	//: one random half for the whole batch, as leaseRows takes one.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing leased.
		return nil, backendFailed("rand", "", entropyErr)
	}
	rows, err := ex.QueryContext(ctx, b.stmts.leaseReturning,
		at.deadline, []byte(lease), at.now, int64(b.policy.MaxDeliveries), int64(max))
	//: the database refused.
	if err != nil {
		//: QueueBackendFailed.
		return nil, b.failed("lease the due messages", err)
	}
	defer func() { err = b.finishRows(rows, "lease the due messages", err) }()
	var wasDue []int64
	//: every row the statement leased; its count is the one it moved on.
	for rows.Next() {
		var id string
		var payload []byte
		var enqueuedAt, deliveries, due int64
		//: a row the scan refused.
		if scanErr := rows.Scan(&id, &enqueuedAt, &deliveries, &payload, &due); scanErr != nil {
			//: QueueBackendFailed.
			return nil, b.failed("lease the due messages", scanErr)
		}
		wasDue = append(wasDue, due)
		batch = append(batch, corequeue.DeliveryValue{
			Message: corequeue.MessageValue{ID: id, Payload: payload, EnqueuedAt: time.Unix(0, enqueuedAt)},
			Lease: corequeue.LeaseValue{
				Receipt: sqlReceiptValue(id, int(deliveries), lease), ExpiresAt: time.Unix(0, at.deadline),
			},
			Deliveries: int(deliveries),
		})
	}
	//: in the order pick reads them: the instant due, then the identifier.
	return sortedByDue(batch, wasDue), nil
}

// sortedByDue orders batch by the instant each delivery was due — due[i] for
// batch[i] — then by identifier.
func sortedByDue(batch []corequeue.DeliveryValue, due []int64) []corequeue.DeliveryValue {
	order := make([]int, len(batch))
	//: the positions, sorted, then the deliveries laid out in their order.
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(x, y int) int {
		//: visibility first, the identifier on a tie.
		return cmp.Or(cmp.Compare(due[x], due[y]), strings.Compare(batch[x].Message.ID, batch[y].Message.ID))
	})
	sorted := make([]corequeue.DeliveryValue, len(batch))
	//: each delivery at its place.
	for at, from := range order {
		sorted[at] = batch[from]
	}
	return sorted
}
