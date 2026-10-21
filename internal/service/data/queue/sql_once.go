package queue

import (
	"context"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// sqlOnceSweepRows bounds how many passed reservations one PublishOnce
// removes besides its own key's: enough to outpace the reservations one call
// adds, and one bounded statement whatever the backlog.
const sqlOnceSweepRows int = 100

// PublishOnce inserts payload into the queue unless key is still reserved, and
// reserves it for window (core/data/queue.OncePublisher), all in one
// transaction: the caller's, as a savepoint of it, or one of the broker's own.
//
// A reservation is a row of the queue's own table — dead = 2, the key's
// SHA-256 as its id, the end of its window as its due — so the table's primary
// key is the unique constraint, and a database that ran SQLMigration before
// this sibling existed needs nothing more. The call removes the key's passed
// reservation, claims the key with an insertion that affects no row when it
// is taken, inserts the message only when the claim did, and removes up to
// sqlOnceSweepRows passed reservations of other keys — those another
// transaction holds are skipped, never waited for, where the engine can.
//
// The reservation and the message commit together or not at all, so on this
// broker a crash costs nothing: no duplicate, no loss. Two producers claiming
// one key at once are ordered by the engine — the second waits on the first's
// row until it commits, then affects nothing, or claims the key if the first
// rolled back.
func (b *sqlBroker) PublishOnce(
	ctx context.Context, key string, payload []byte, window time.Duration,
) (message corequeue.MessageValue, published bool, err error) {
	//: a cancelled producer.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, false, ctx.Err()
	}
	//: the bound Publish enforces, before a byte is sent.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, false, tooLarge
	}
	entropy, entropyErr := randomHex(idEntropyBytes)
	//: the message's random half, as Publish mints it.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing was sent.
		return corequeue.MessageValue{}, false, backendFailed("rand", "", entropyErr)
	}
	now := b.clk.Now()
	//: an instant an identifier cannot carry.
	if !nameable(now) {
		//: QueueMisconfigured, naming the clock.
		return corequeue.MessageValue{}, false, clockRefused(now)
	}
	until, invalid := onceUntil(now, key, window)
	//: a key or a window no broker honours.
	if invalid != nil {
		//: QueueMisconfigured, and nothing is sent.
		return corequeue.MessageValue{}, false, invalid
	}
	id := messageID(now.UnixNano(), entropy)
	digest := []byte(onceDigest(key))
	runErr := b.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: what this run of the work decided, and nothing a run before it did.
		published = false
		claimed, claimErr := b.claimOnce(txCtx, ex, digest, until.UnixNano(), now.UnixNano())
		//: the claim did not complete.
		if claimErr != nil {
			//: QueueBackendFailed, the driver's text withheld.
			return claimErr
		}
		//: the key is this publication's: the message joins it.
		if claimed {
			//: one row: live, queued, visible now, never delivered.
			if _, execErr := ex.ExecContext(txCtx, b.stmts.insert, []byte(id), now.UnixNano(), now.UnixNano(), payload); execErr != nil {
				//: QueueBackendFailed — the claim rolls back with it.
				return b.failed("publish", execErr)
			}
			published = true
		}
		//: the passed reservations of other keys, a bounded number.
		return b.sweepOnce(txCtx, ex, now.UnixNano())
	})
	//: nothing was reserved, and nothing published.
	if runErr != nil {
		//: QueueBackendFailed, or the transactor's own verdict.
		return corequeue.MessageValue{}, false, runErr
	}
	//: an earlier publication's window still holds the key.
	if !published {
		//: a duplicate, and that is the answer.
		return corequeue.MessageValue{}, false, nil
	}
	b.announce(ctx)
	//: published once the transaction commits; the Payload is the caller's.
	return corequeue.MessageValue{ID: id, Payload: payload, EnqueuedAt: now}, true, nil
}

// claimOnce removes the key's reservation when its window has passed by now,
// then reserves the key until until, and reports whether the claim affected a
// row — whether the key is this call's.
func (b *sqlBroker) claimOnce(ctx context.Context, ex coresql.Executor, digest []byte, until, now int64) (bool, error) {
	//: a passed reservation holds nothing; its row is the key's to reclaim.
	if _, execErr := ex.ExecContext(ctx, b.stmts.once.drop, digest, now); execErr != nil {
		//: QueueBackendFailed.
		return false, b.failed("drop a passed reservation", execErr)
	}
	result, execErr := ex.ExecContext(ctx, b.stmts.once.claim, digest, until, now)
	n, err := rowsAffected(result, execErr)
	//: the claim did not complete.
	if err != nil {
		//: QueueBackendFailed.
		return false, b.failed("reserve a key", err)
	}
	//: one row is the key reserved; none is the key still held.
	return n == 1, nil
}

// sweepOnce removes up to sqlOnceSweepRows reservations whose window has
// passed by now, of any key: read locked and skipping those another
// transaction holds, then removed by identifier.
func (b *sqlBroker) sweepOnce(ctx context.Context, ex coresql.Executor, now int64) error {
	ids, err := b.pickPassedOnce(ctx, ex, now)
	//: the read failed, or there is nothing to remove.
	if err != nil || len(ids) == 0 {
		//: QueueBackendFailed, or nil.
		return err
	}
	//: removed, still passed: the instant first, then the identifiers.
	if _, execErr := ex.ExecContext(ctx, b.stmts.onceSweepRows(len(ids)), append([]any{now}, ids...)...); execErr != nil {
		//: QueueBackendFailed.
		return b.failed("remove the passed reservations", execErr)
	}
	//: swept.
	return nil
}

// pickPassedOnce reads the identifiers of up to sqlOnceSweepRows reservations
// whose window has passed by now — locked, and skipping those another
// transaction holds, where the engine can.
func (b *sqlBroker) pickPassedOnce(ctx context.Context, ex coresql.Executor, now int64) (ids []any, err error) {
	rows, err := ex.QueryContext(ctx, b.stmts.once.pick, now, int64(sqlOnceSweepRows))
	//: the read did not start.
	if err != nil {
		//: QueueBackendFailed.
		return nil, b.failed("read the passed reservations", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = b.finishRows(rows, "read the passed reservations", err) }()
	//: their identifiers, in the order read.
	for rows.Next() {
		var id []byte
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&id); scanErr != nil {
			//: QueueBackendFailed.
			return nil, b.failed("read the passed reservations", scanErr)
		}
		ids = append(ids, id)
	}
	//: the finisher reports a failure that ended the rows early.
	return ids, nil
}
