package queue

import (
	"context"
	"iter"
	"slices"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// sqlStatsGroups is how many groups the stats statement can answer: live
// without a lease, live with one, and dead.
const sqlStatsGroups int = 3

// Stats counts the table by state and lease in one statement, reading no
// payload (core/data/queue.Counter). Inside the caller's transaction it
// counts what that transaction sees.
func (b *sqlBroker) Stats(ctx context.Context) (stats corequeue.StatsValue, err error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.StatsValue{}, ctx.Err()
	}
	ex, _ := b.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, b.stmts.bulk.stats)
	//: the database refused.
	if err != nil {
		//: QueueBackendFailed.
		return corequeue.StatsValue{}, b.failed("count the queue", err)
	}
	defer func() { err = b.finishRows(rows, "count the queue", err) }()
	var oldest int64
	//: one row per group, at most three.
	for range sqlStatsGroups + 1 {
		//: no more groups.
		if !rows.Next() {
			break
		}
		var dead, leased, count, firstEnqueued, firstDue int64
		//: a row the scan refused.
		if scanErr := rows.Scan(&dead, &leased, &count, &firstEnqueued, &firstDue); scanErr != nil {
			//: QueueBackendFailed.
			return corequeue.StatsValue{}, b.failed("count the queue", scanErr)
		}
		//: the three groups the statement answers.
		switch {
		//: the dead letters.
		case dead != 0:
			stats.Dead += count
			continue
		//: the leased, whose due is a lease's deadline and not a visibility.
		case leased != 0:
			stats.Leased += count
		//: the waiting, whose due is when the first becomes visible.
		default:
			stats.Queued += count
			stats.NextDueAt = time.Unix(0, firstDue)
		}
		oldest = earliest(oldest, firstEnqueued)
	}
	//: no live row has no oldest one.
	if oldest != 0 {
		stats.OldestEnqueuedAt = time.Unix(0, oldest)
	}
	//: a snapshot, as Messages is.
	return stats, nil
}

// Purge deletes every live row — those without a lease, then those with one —
// in one transaction, and no dead letter or PublishOnce reservation
// (core/data/queue.Purger).
func (b *sqlBroker) Purge(ctx context.Context) (corequeue.PurgeValue, error) {
	//: a cancelled caller must not destroy what nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.PurgeValue{}, ctx.Err()
	}
	var purged corequeue.PurgeValue
	err := b.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		queued, queuedErr := rowsAffected(ex.ExecContext(txCtx, b.stmts.bulk.purgeQueued))
		//: the database refused.
		if queuedErr != nil {
			//: QueueBackendFailed.
			return b.failed("purge the queue", queuedErr)
		}
		leased, leasedErr := rowsAffected(ex.ExecContext(txCtx, b.stmts.bulk.purgeLeased))
		//: the database refused.
		if leasedErr != nil {
			//: QueueBackendFailed.
			return b.failed("purge the queue", leasedErr)
		}
		purged = corequeue.PurgeValue{Queued: int(queued), Leased: int(leased)}
		return nil
	})
	//: rolled back: nothing was purged.
	if err != nil {
		//: QueueBackendFailed.
		return corequeue.PurgeValue{}, err
	}
	//: an idle consumer reads the emptied queue — after the caller's commit.
	b.announce(ctx)
	//: what was there.
	return purged, nil
}

// ReplayDeadLetters replays up to max dead letters, oldest enqueued first, in
// one statement (core/data/queue.DeadLetterBulkManager): atomic, so it never
// fails part-way.
func (b *sqlBroker) ReplayDeadLetters(ctx context.Context, max int) (replayed int, err error) {
	//: the same refusal Receive makes.
	if invalid := checkBatch(max); invalid != nil {
		//: InvalidBatchSize.
		return 0, invalid
	}
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return 0, ctx.Err()
	}
	now := b.clk.Now().UnixNano()
	err = b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		n, execErr := rowsAffected(ex.ExecContext(txCtx, b.stmts.bulk.replayMany, now, int64(max)))
		//: the database refused.
		if execErr != nil {
			//: QueueBackendFailed.
			return b.failed("replay dead letters", execErr)
		}
		replayed = int(n)
		return nil
	})
	//: nothing moved.
	if err != nil {
		//: QueueBackendFailed.
		return 0, err
	}
	//: queued again: an idle consumer looks — after the caller's commit.
	if replayed > 0 {
		b.announce(ctx)
	}
	//: how many.
	return replayed, nil
}

// PurgeDeadLetters deletes up to max dead letters that failed before before,
// oldest enqueued first, in one statement
// (core/data/queue.DeadLetterBulkManager).
func (b *sqlBroker) PurgeDeadLetters(ctx context.Context, before time.Time, max int) (removed int, err error) {
	//: the same refusal Receive makes.
	if invalid := checkBatch(max); invalid != nil {
		//: InvalidBatchSize.
		return 0, invalid
	}
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return 0, ctx.Err()
	}
	err = b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		n, execErr := rowsAffected(ex.ExecContext(txCtx, b.stmts.bulk.purgeDeadMany, before.UnixNano(), int64(max)))
		//: the database refused.
		if execErr != nil {
			//: QueueBackendFailed.
			return b.failed("purge dead letters", execErr)
		}
		removed = int(n)
		return nil
	})
	//: how many, none when it failed.
	return removed, err
}

// sweepRetention deletes every dead letter past the policy's
// DeadLetterRetention, at most once a minute per broker on its clock, in one
// statement on the transaction ctx carries or the pool.
func (b *sqlBroker) sweepRetention(ctx context.Context, now int64) error {
	swept := b.deadSwept.Load()
	//: no retention, a sweep less than a minute ago, or another worker of
	//: this broker sweeping now.
	if !retentionDue(b.policy, swept, now) || !b.deadSwept.CompareAndSwap(swept, now) {
		return nil
	}
	//: one DELETE.
	return b.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		//: the database refused.
		if _, execErr := ex.ExecContext(txCtx, b.stmts.bulk.retention, retentionCutoff(b.policy, now)); execErr != nil {
			//: QueueBackendFailed.
			return b.failed("delete expired dead letters", execErr)
		}
		return nil
	})
}

// expireDue buries every due row past the policy's MaxAge with
// MESSAGE_EXPIRED, in the lease's transaction, before its first pick. A
// policy with no MaxAge sends nothing.
func (b *sqlBroker) expireDue(ctx context.Context, ex coresql.Executor, now int64) error {
	//: zero keeps a message for good.
	if b.policy.MaxAge <= 0 {
		return nil
	}
	args := append(b.stmts.causeArgs(now, expiredCause()), now-int64(b.policy.MaxAge), now)
	//: the database refused.
	if _, execErr := ex.ExecContext(ctx, b.stmts.bulk.expire, args...); execErr != nil {
		//: QueueBackendFailed.
		return b.failed("dead-letter expired messages", execErr)
	}
	//: buried, or none to bury.
	return nil
}

// PublishMany inserts every payload in multi-row INSERTs — one when the batch
// fits one statement — on the transaction ctx carries, or in one of the
// broker's own when it takes several, so the batch exists entirely or not
// at all (core/data/queue.BatchPublisher).
func (b *sqlBroker) PublishMany(ctx context.Context, payloads [][]byte) ([]corequeue.MessageValue, error) {
	//: a cancelled producer must not publish.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return nil, ctx.Err()
	}
	//: every payload before any is written.
	if tooLarge := checkSizes(payloads, b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, nothing published.
		return nil, tooLarge
	}
	//: nothing to publish is not an error.
	if len(payloads) == 0 {
		return nil, nil
	}
	now := b.clk.Now()
	//: an instant the identifier and the columns cannot carry.
	if !nameable(now) {
		//: QueueMisconfigured, naming the clock.
		return nil, clockRefused(now)
	}
	messages, args, mintErr := mintBatch(payloads, now)
	//: the identifiers' random halves.
	if mintErr != nil {
		//: QueueBackendFailed — nothing was written.
		return nil, mintErr
	}
	perStatement := bulkRowsPerStatement(sqlInsertArgs)
	insertErr := b.run(ctx, len(payloads) > perStatement, func(txCtx context.Context, ex coresql.Executor) error {
		//: one INSERT per chunk of rows.
		for chunk := range slices.Chunk(args, perStatement*sqlInsertArgs) {
			//: the database refused: the transaction rolls the batch back.
			if _, execErr := ex.ExecContext(txCtx, b.stmts.bulk.insertRows(len(chunk)/sqlInsertArgs), chunk...); execErr != nil {
				//: QueueBackendFailed.
				return b.failed("publish a batch", execErr)
			}
		}
		return nil
	})
	//: nothing was published.
	if insertErr != nil {
		//: QueueBackendFailed.
		return nil, insertErr
	}
	//: one wake for the batch — after the caller's commit.
	b.announce(ctx)
	//: one message per payload.
	return messages, nil
}

// mintBatch mints a message for every payload, enqueued at now, and the
// arguments of their rows: identifier, due, enqueue instant, payload.
func mintBatch(payloads [][]byte, now time.Time) ([]corequeue.MessageValue, []any, error) {
	messages := make([]corequeue.MessageValue, len(payloads))
	args := make([]any, 0, len(payloads)*sqlInsertArgs)
	//: in order.
	for index, payload := range payloads {
		entropy, entropyErr := randomHex(idEntropyBytes)
		//: the identifier's random half, as Publish mints it.
		if entropyErr != nil {
			//: QueueBackendFailed.
			return nil, nil, backendFailed("rand", "", entropyErr)
		}
		id := messageID(now.UnixNano(), entropy)
		messages[index] = corequeue.MessageValue{ID: id, Payload: payload, EnqueuedAt: now}
		args = append(args, []byte(id), now.UnixNano(), now.UnixNano(), payload)
	}
	//: the batch, minted.
	return messages, args, nil
}

// AckMany deletes the rows every receipt holds in one statement per chunk of
// receipts, and reads which landed from the rows it deleted
// (core/data/queue.Acker): RETURNING on PostgreSQL and SQLite, a locking read
// first on MySQL. A receipt this broker cannot parse is never sent.
func (b *sqlBroker) AckMany(ctx context.Context, receipts []corequeue.ReceiptValue) (corequeue.AckResultValue, error) {
	result := corequeue.AckResultValue{Outcomes: make([]corequeue.AckOutcome, len(receipts))}
	//: a cancelled caller acknowledges nothing; every outcome says so.
	if ctx.Err() != nil {
		//: the caller's deadline, nothing attempted.
		return result, ctx.Err()
	}
	held := make([]sqlReceipt, 0, len(receipts))
	positions := make([]int, 0, len(receipts))
	//: the receipts this broker could have issued, and where they sit.
	for index, receipt := range receipts {
		parsed, ok := parseSQLReceipt(string(receipt))
		//: never this broker's: an outcome, never sent.
		if !ok {
			result.Outcomes[index] = corequeue.AckUnknownReceipt
			continue
		}
		held, positions = append(held, parsed), append(positions, index)
	}
	//: nothing the database could hold.
	if len(held) == 0 {
		return result, nil
	}
	now := b.clk.Now().UnixNano()
	perStatement := bulkRowsPerStatement(sqlAckArgs)
	several := len(held) > perStatement || b.stmts.dialect == coresql.DialectMySQL
	landed := map[sqlLanded]bool{}
	ackErr := b.run(ctx, several, func(txCtx context.Context, ex coresql.Executor) error {
		clear(landed)
		//: one statement — two on MySQL — per chunk of receipts.
		for chunk := range slices.Chunk(held, perStatement) {
			//: the database refused: nothing of a transaction lands.
			if err := b.ackChunk(txCtx, ex, now, chunk, landed); err != nil {
				return err
			}
		}
		return nil
	})
	//: rolled back, or the pool's statement failed: no outcome is known.
	if ackErr != nil {
		//: QueueBackendFailed, every sent receipt not attempted.
		return result, ackErr
	}
	//: each sent receipt, landed once, or found lapsed.
	for position, receipt := range held {
		key := sqlLanded{id: receipt.id, lease: receipt.lease}
		result.Outcomes[positions[position]] = corequeue.AckLeaseExpired
		//: the first receipt naming a row that went takes it.
		if landed[key] {
			result.Outcomes[positions[position]] = corequeue.AckAcknowledged
			delete(landed, key)
		}
	}
	//: one outcome per receipt.
	return result, nil
}

// sqlLanded names a row AckMany deleted: its identifier and the lease it was
// held under.
type sqlLanded struct {
	id, lease string
}

// ackChunk deletes the rows chunk holds and records each one deleted in
// landed.
func (b *sqlBroker) ackChunk(
	ctx context.Context, ex coresql.Executor, now int64, chunk []sqlReceipt, landed map[sqlLanded]bool,
) error {
	args := make([]any, 0, 1+len(chunk)*sqlAckArgs)
	args = append(args, now)
	//: identifier, lease and count per receipt, as Ack binds them.
	for _, receipt := range chunk {
		args = append(args, []byte(receipt.id), []byte(receipt.lease), int64(receipt.deliveries))
	}
	read := b.stmts.bulk.ackRows(len(chunk))
	//: MySQL has no RETURNING: the rows are read, locked, first.
	if b.stmts.dialect == coresql.DialectMySQL {
		read = b.stmts.bulk.ackPick(len(chunk))
	}
	//: the rows that are, or are about to be, deleted.
	if readErr := b.readLanded(ctx, ex, read, args, landed); readErr != nil {
		return readErr
	}
	//: MySQL's DELETE, after its read.
	if b.stmts.dialect == coresql.DialectMySQL {
		//: the database refused.
		if _, execErr := ex.ExecContext(ctx, b.stmts.bulk.ackRows(len(chunk)), args...); execErr != nil {
			//: QueueBackendFailed.
			return b.failed("acknowledge a batch", execErr)
		}
	}
	return nil
}

// readLanded runs query and records the (identifier, lease) of every row it
// returns in landed.
func (b *sqlBroker) readLanded(
	ctx context.Context, ex coresql.Executor, query string, args []any, landed map[sqlLanded]bool,
) (err error) {
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the database refused.
	if err != nil {
		//: QueueBackendFailed.
		return b.failed("acknowledge a batch", err)
	}
	defer func() { err = b.finishRows(rows, "acknowledge a batch", err) }()
	//: every row the statement names.
	for rows.Next() {
		var key sqlLanded
		//: a row the scan refused; the two columns are read as the strings
		//: a receipt names them by.
		if scanErr := rows.Scan(&key.id, &key.lease); scanErr != nil {
			//: QueueBackendFailed.
			return b.failed("acknowledge a batch", scanErr)
		}
		landed[key] = true
	}
	return nil
}

// AllMessages yields the live rows a page at a time, each page one unlocked
// read after the last identifier (core/data/queue.MessageLister).
func (b *sqlBroker) AllMessages(ctx context.Context, after string) iter.Seq2[corequeue.LiveMessageValue, error] {
	//: Messages' pages, threaded by the iterator.
	return pagedMessages(ctx, after, b.Messages)
}
