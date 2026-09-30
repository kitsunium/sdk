// Package queue — the SQL broker's dead letters: burying one a handler
// rejected, reading them back, and the two decisions an operator takes.
package queue

import (
	"context"
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// Reject dead-letters the leased message at once with cause, whatever its
// delivery count (core/queue.Rejecter): the burial a nack on the last attempt
// makes, reached sooner, in one statement.
func (b *sqlBroker) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	held, err := b.receiptOf(ctx, receipt)
	//: the same refusals Ack makes, and nothing moves.
	if err != nil {
		//: UnknownReceipt, or the caller's deadline.
		return err
	}
	//: dead with the handler's cause, at the count it had.
	return b.buryHeld(ctx, held, describeCause(cause), b.clk.Now())
}

// DeadLetters returns up to max dead letters, in the order they died, and
// removes none of them. Inside a transaction its context carries, it reads
// what that transaction sees.
func (b *sqlBroker) DeadLetters(
	ctx context.Context, max int,
) (dead []corequeue.DeadLetterValue, err error) {
	//: the same refusal Receive makes.
	if invalid := checkBatch(max); invalid != nil {
		//: InvalidBatchSize.
		return nil, invalid
	}
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return nil, ctx.Err()
	}
	ex, _ := b.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, b.stmts.deadList, int64(max))
	//: the read did not start.
	if err != nil {
		//: QueueBackendFailed.
		return nil, b.failed("read the dead letters", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = b.finishRows(rows, "read the dead letters", err) }()
	//: one dead letter per row; nil until one is found.
	for rows.Next() {
		record, scanErr := scanDeadLetter(rows)
		//: a row the driver cannot hand over fails the read.
		if scanErr != nil {
			//: QueueBackendFailed.
			return nil, b.failed("read the dead letters", scanErr)
		}
		dead = append(dead, record)
	}
	//: evidence, still in the table.
	return dead, nil
}

// scanner is what reading one row needs of *sql.Rows.
type scanner interface {
	// Scan copies the row's columns into dest.
	Scan(dest ...any) error
}

// scanDeadLetter reads one dead letter's row.
func scanDeadLetter(rows scanner) (corequeue.DeadLetterValue, error) {
	var id, reason, cause, payload []byte
	var enqueuedAt, deliveries, failedAt int64
	var code stdsql.NullInt64
	//: in the order the statement names the columns.
	if err := rows.Scan(&id, &enqueuedAt, &deliveries, &failedAt, &reason, &cause, &code, &payload); err != nil {
		//: the driver's failure, for the caller to wrap.
		return corequeue.DeadLetterValue{}, err
	}
	//: the record, with the cause the burial kept.
	return corequeue.DeadLetterValue{
		Message: corequeue.MessageValue{
			ID: string(id), Payload: payload, EnqueuedAt: time.Unix(0, enqueuedAt),
		},
		FailedAt: time.Unix(0, failedAt), Deliveries: int(deliveries),
		Reason: string(reason), Cause: string(cause), Code: uint32(code.Int64), //nolint:gosec // written from a uint32
	}, nil
}

// ReplayDeadLetter queues the dead letter id again, visible now, with its
// delivery count reset (core/queue.DeadLetterManager). It keeps its ID, its
// payload and its enqueue instant; its cause is cleared with its death.
//
// It is one statement that changes the row's state, so two replays racing each
// other cannot both queue it: the second finds no dead letter and is refused
// DEAD_LETTER_NOT_FOUND. Inside a transaction its context carries, the replay
// stands or falls with it, and the consumers are woken after its commit.
func (b *sqlBroker) ReplayDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller must not queue a message nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	now := b.clk.Now().UnixNano()
	err := b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		result, execErr := ex.ExecContext(txCtx, b.stmts.replay, now, []byte(id))
		//: queued, DeadLetterNotFound, or QueueBackendFailed.
		return b.deadLetterMoved(result, execErr, "replay a dead letter", id)
	})
	//: nothing was queued.
	if err != nil {
		//: DeadLetterNotFound or QueueBackendFailed.
		return err
	}
	b.announce(ctx)
	//: queued; its next delivery counts 1.
	return nil
}

// DeleteDeadLetter removes the dead letter id for good
// (core/queue.DeadLetterManager).
func (b *sqlBroker) DeleteDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	//: one statement: only a dead letter goes.
	return b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		result, execErr := ex.ExecContext(txCtx, b.stmts.deleteDead, []byte(id))
		//: gone, DeadLetterNotFound, or QueueBackendFailed.
		return b.deadLetterMoved(result, execErr, "delete a dead letter", id)
	})
}

// deadLetterMoved turns a statement on one dead letter into the call's
// verdict: one row is the dead letter acted on, none is one the store does not
// hold.
func (b *sqlBroker) deadLetterMoved(result stdsql.Result, execErr error, step, id string) error {
	n, err := rowsAffected(result, execErr)
	//: the statement did not complete, or its count could not be read.
	if err != nil {
		//: QueueBackendFailed, the driver's text withheld.
		return b.failed(step, err)
	}
	//: never dead, or already replayed or deleted.
	if n == 0 {
		//: DeadLetterNotFound.
		return deadLetterNotFound(sqlBrokerName, id)
	}
	//: acted on.
	return nil
}
