package queue

import (
	"context"
	"iter"
	"slices"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// DeadLettersAfter returns up to max dead letters whose identifier sorts after
// after, in the byte order of their identifiers, and removes none of them
// (core/data/queue.DeadLetterPager).
func (b *memoryBroker) DeadLettersAfter(
	ctx context.Context, after string, max int,
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
	//: a genuine READ: an investigation must not serialise against the
	//: consumers it is investigating.
	b.mu.RLock()
	defer b.mu.RUnlock()
	from := b.deadAfter(after)
	//: a dead letter is evidence; reading it must not consume it, and the
	//: outer copy keeps the nil an empty store has always returned.
	dead = slices.Clone(b.dead[from:min(from+max, len(b.dead))])
	//: nor let the reader rewrite it: the payload and the trail are copied,
	//: as Receive copies, so what the caller edits is its own.
	for index := range dead {
		dead[index].Message.Payload = slices.Clone(dead[index].Message.Payload)
		dead[index].Trail = cloneTrail(dead[index].Trail)
	}
	//: the caller's own, in the order the store holds them.
	return dead, nil
}

// deadAfter is the index of the first dead letter whose identifier sorts
// after after. The caller holds mu.
func (b *memoryBroker) deadAfter(after string) int {
	at, found := slices.BinarySearchFunc(b.dead, after, compareDeadID)
	//: an identifier the store holds is where the last page ended: skip it.
	if found {
		//: the one after it.
		return at + 1
	}
	//: the insertion point is the first one after.
	return at
}

// Stats counts the queue as the heap holds it (core/data/queue.Counter).
func (b *memoryBroker) Stats(ctx context.Context) (corequeue.StatsValue, error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.StatsValue{}, ctx.Err()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	//: each level's list is ordered by visibility, so the earliest head is
	//: the next due.
	stats := corequeue.StatsValue{
		Queued: int64(b.readyCount()), Leased: int64(len(b.inflight)), Dead: int64(len(b.dead)),
		NextDueAt: b.earliestReady(),
	}
	//: the oldest among the waiting, then among the leased.
	for _, list := range b.ready {
		//: every level's.
		for _, record := range list {
			stats.OldestEnqueuedAt = earlierOf(stats.OldestEnqueuedAt, record.enqueuedAt)
		}
	}
	//: a map has no order; the leased set is a lease's worth of messages.
	for _, record := range b.inflight {
		stats.OldestEnqueuedAt = earlierOf(stats.OldestEnqueuedAt, record.enqueuedAt)
	}
	//: a snapshot, as Messages is.
	return stats, nil
}

// earlierOf is the earlier of two instants, the zero time.Time standing for
// none.
func earlierOf(held, candidate time.Time) time.Time {
	//: the first one seen, or an earlier one.
	if held.IsZero() || candidate.Before(held) {
		//: the candidate.
		return candidate
	}
	//: the one held.
	return held
}

// Purge removes every live message, waiting and leased, and no dead letter
// (core/data/queue.Purger).
func (b *memoryBroker) Purge(ctx context.Context) (corequeue.PurgeValue, error) {
	//: a cancelled caller must not destroy what nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.PurgeValue{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	purged := corequeue.PurgeValue{Queued: b.readyCount(), Leased: len(b.inflight)}
	clear(b.ready[:])
	//: no lease is left, so neither is any deadline.
	clear(b.inflight)
	b.expiry = newLeaseExpiries()
	//: an idle consumer reads the emptied queue at once.
	b.wake.fire()
	//: what was there.
	return purged, nil
}

// ReplayDeadLetters replays up to max dead letters, oldest enqueued first
// (core/data/queue.DeadLetterBulkManager). Under one lock it cannot fail
// part-way.
func (b *memoryBroker) ReplayDeadLetters(ctx context.Context, max int) (replayed int, err error) {
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
	b.mu.Lock()
	defer b.mu.Unlock()
	replayed = min(max, len(b.dead))
	now := b.clk.Now()
	//: the oldest first, each as ReplayDeadLetter replays one.
	for _, dead := range b.dead[:replayed] {
		b.requeueDead(dead, now)
	}
	b.dead = slices.Delete(b.dead, 0, replayed)
	//: queued again: an idle consumer looks now.
	if replayed > 0 {
		b.wake.fire()
	}
	//: how many.
	return replayed, nil
}

// requeueDead puts dead back into the queue, visible at now, its count reset,
// keeping its identifier, payload and enqueue instant. The caller holds mu
// and has taken dead out of the store.
func (b *memoryBroker) requeueDead(dead corequeue.DeadLetterValue, now time.Time) {
	b.seq++
	b.insertReady(&memRecord{
		payload: dead.Message.Payload, id: dead.Message.ID,
		enqueuedAt: dead.Message.EnqueuedAt, visibleAt: now, seq: b.seq, priority: dead.Priority,
	})
}

// PurgeDeadLetters deletes up to max dead letters that failed before before,
// oldest enqueued first (core/data/queue.DeadLetterBulkManager).
func (b *memoryBroker) PurgeDeadLetters(ctx context.Context, before time.Time, max int) (removed int, err error) {
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
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dead = slices.DeleteFunc(b.dead, func(dead corequeue.DeadLetterValue) bool {
		//: the first max that failed before the instant, in list order.
		if removed < max && dead.FailedAt.Before(before) {
			removed++
			return true
		}
		return false
	})
	//: how many.
	return removed, nil
}

// sweepRetention removes every dead letter past the policy's
// DeadLetterRetention, at most once a minute on the broker's clock. The
// caller holds mu.
func (b *memoryBroker) sweepRetention(now time.Time) {
	//: no retention, or a sweep less than a minute ago.
	if !retentionDue(b.policy, b.swept, now.UnixNano()) {
		return
	}
	b.swept = now.UnixNano()
	cutoff := time.Unix(0, retentionCutoff(b.policy, now.UnixNano()))
	b.dead = slices.DeleteFunc(b.dead, func(dead corequeue.DeadLetterValue) bool {
		//: failed at or before the cutoff: kept as long as the policy said.
		return !dead.FailedAt.After(cutoff)
	})
}

// expired reports whether a message enqueued at enqueuedAt has reached the
// policy's MaxAge by now.
func (b *memoryBroker) expired(enqueuedAt, now time.Time) bool {
	//: the one rule the three brokers apply.
	return messageExpired(b.policy, enqueuedAt.UnixNano(), now.UnixNano())
}

// PublishMany appends every payload under one lock and wakes once
// (core/data/queue.BatchPublisher). It cannot fail part-way: every payload is
// checked first, and nothing else can refuse.
func (b *memoryBroker) PublishMany(ctx context.Context, payloads [][]byte) ([]corequeue.MessageValue, error) {
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
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	messages := make([]corequeue.MessageValue, len(payloads))
	//: in order, under one lock.
	for index, payload := range payloads {
		messages[index] = b.enqueueQuiet(payload, now, now, corequeue.PriorityNormal)
	}
	//: one wake for the batch.
	b.wake.fire()
	//: one message per payload.
	return messages, nil
}

// AckMany acknowledges every receipt under one lock, one outcome each
// (core/data/queue.Acker).
func (b *memoryBroker) AckMany(ctx context.Context, receipts []corequeue.ReceiptValue) (corequeue.AckResultValue, error) {
	result := corequeue.AckResultValue{Outcomes: make([]corequeue.AckOutcome, len(receipts))}
	//: a cancelled caller acknowledges nothing; every outcome says so.
	if ctx.Err() != nil {
		//: the caller's deadline, nothing attempted.
		return result, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	//: each as Ack resolves it.
	for index, receipt := range receipts {
		record, resolveErr := b.resolve(receipt)
		//: refused: an outcome, never the call's error.
		if resolveErr != nil {
			result.Outcomes[index] = ackOutcomeOf(resolveErr)
			continue
		}
		b.unlease(record)
		result.Outcomes[index] = corequeue.AckAcknowledged
	}
	//: one outcome per receipt.
	return result, nil
}

// AllMessages yields the live messages a page at a time
// (core/data/queue.MessageLister).
func (b *memoryBroker) AllMessages(ctx context.Context, after string) iter.Seq2[corequeue.LiveMessageValue, error] {
	//: Messages' pages, threaded by the iterator.
	return pagedMessages(ctx, after, b.Messages)
}

// ackOutcomeOf is the outcome of a receipt Ack refused with err: a lapsed
// lease, or one this broker never issued.
func ackOutcomeOf(err error) corequeue.AckOutcome {
	//: the receipt was never this broker's.
	if kerrs.HasCode(err, corequeue.CodeUnknownReceipt) {
		return corequeue.AckUnknownReceipt
	}
	//: LeaseExpired, the one other refusal Ack makes.
	return corequeue.AckLeaseExpired
}
