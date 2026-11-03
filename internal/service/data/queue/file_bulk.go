package queue

import (
	"bytes"
	"context"
	"io"
	"iter"
	"path"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
)

// deadSweepAll is the bound of a retention sweep: every record the directory
// holds.
const deadSweepAll int = int(^uint(0) >> 1)

// deadHeaderMax bounds what a sweep reads of a dead-letter record to find its
// failure instant: a header this package writes is well under it — seven
// short lines, a trail of sixteen codes, a reason and a cause of 120 runes —
// so the payload behind it is never read, and a record whose header does not
// end within it is not one this package wrote.
const deadHeaderMax int = 4096

// DeadLettersAfter returns up to max dead letters whose identifier sorts after
// after, in name order — which, the name opening with the zero-padded enqueue
// instant and the entropy the identifier is made of, is identifier order
// (core/data/queue.DeadLetterPager). It reads the directory once.
func (b *fileBroker) DeadLettersAfter(
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
	entries, listErr := b.namesOf(dirDead)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return nil, listErr
	}
	//: nil until something is found, so an empty store allocates nothing.
	var out []corequeue.DeadLetterValue
	//: oldest enqueued first, from the cursor on.
	for _, base := range entries {
		name, named := parseDead(base)
		//: not one of ours, or at or before where the listing resumes.
		if !named || name.ID() <= after {
			continue
		}
		record, ok, readErr := b.readDead(base)
		//: the medium refused mid-listing.
		if readErr != nil {
			//: QueueBackendFailed.
			return nil, readErr
		}
		//: a record another process removed or is still publishing.
		if !ok {
			continue
		}
		out = append(out, record)
		//: the caller asked for a bounded batch.
		if len(out) == max {
			break
		}
	}
	//: evidence, still on disk.
	return out, nil
}

// Purge unlinks every queued, then every in-flight message, and no dead
// letter (core/data/queue.Purger). Queued first: a message a consumer leases
// meanwhile moves into flight, which is read after, so it is not missed. A
// name gone by the time it is unlinked was taken by somebody else and is not
// counted.
func (b *fileBroker) Purge(ctx context.Context) (corequeue.PurgeValue, error) {
	//: a cancelled caller must not destroy what nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.PurgeValue{}, ctx.Err()
	}
	queued, queuedErr := b.unlinkLive(dirReady)
	//: the medium refused.
	if queuedErr != nil {
		//: QueueBackendFailed, with what was removed so far counted.
		return corequeue.PurgeValue{Queued: queued}, queuedErr
	}
	leased, leasedErr := b.unlinkLive(dirInflight)
	//: whatever was removed, an idle consumer reads the queue again now.
	if queued+leased > 0 {
		b.wake.fire()
	}
	//: what was there, and the medium's refusal if it refused.
	return corequeue.PurgeValue{Queued: queued, Leased: leased}, leasedErr
}

// unlinkLive removes every message dir holds by the state's grammar and
// counts the ones it removed.
func (b *fileBroker) unlinkLive(dir string) (removed int, err error) {
	entries, listErr := b.namesOf(dir)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return 0, listErr
	}
	//: every message the grammar names, and nothing else.
	for _, base := range entries {
		//: a temporary a publication is still writing is not a message yet.
		if _, ok := parseLive(dir, base); !ok {
			continue
		}
		removeErr := b.root.Remove(path.Join(dir, base))
		//: gone already: acknowledged, leased or reclaimed meanwhile.
		if removeErr != nil {
			//: QueueBackendFailed when it is the medium.
			if missing := ignoreMissing("remove", base, removeErr); missing != nil {
				return removed, missing
			}
			continue
		}
		removed++
	}
	//: how many this call removed.
	return removed, nil
}

// ReplayDeadLetters replays up to max dead letters, oldest enqueued first,
// over one read of the dead-letter directory
// (core/data/queue.DeadLetterBulkManager). Each is replayed as
// ReplayDeadLetter replays one — the queued file published first, the
// records removed after —, so a failure or a cancellation part-way leaves
// every dead letter not counted still dead.
func (b *fileBroker) ReplayDeadLetters(ctx context.Context, max int) (replayed int, err error) {
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
	entries, listErr := b.namesOf(dirDead)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return 0, listErr
	}
	//: one message at a time, its records together, oldest first.
	for id, records := range deadMessages(entries) {
		//: the caller stopped asking: what is done is counted.
		if ctx.Err() != nil {
			return replayed, ctx.Err()
		}
		queued, replayErr := b.replayRecords(id, records)
		//: the medium refused part-way.
		if replayErr != nil {
			//: QueueBackendFailed, with the count so far.
			return replayed, replayErr
		}
		//: a record removed meanwhile, or unreadable: not counted.
		if !queued {
			continue
		}
		replayed++
		//: the batch the caller asked for.
		if replayed == max {
			break
		}
	}
	//: how many.
	return replayed, nil
}

// deadMessages groups the dead-letter records of entries by the message they
// name, in name order: the records of one message share the name's leading
// instant and entropy, so they are adjacent.
func deadMessages(entries []string) iter.Seq2[string, []string] {
	return func(yield func(string, []string) bool) {
		var id string
		var records []string
		//: every record the grammar accepts, its message's identifier first.
		for _, base := range entries {
			name, named := parseDead(base)
			//: a temporary, a stray.
			if !named {
				continue
			}
			//: the next message: hand the previous one over first.
			if name.ID() != id && len(records) > 0 {
				//: the caller stopped.
				if !yield(id, records) {
					return
				}
				records = nil
			}
			id = name.ID()
			records = append(records, base)
		}
		//: the last message, if any; nothing follows it either way.
		if len(records) > 0 && !yield(id, records) {
			//: the caller stopped.
			return
		}
	}
}

// PurgeDeadLetters deletes up to max dead letters whose failure instant is
// before before, oldest enqueued first, over one read of the directory and
// one read of each record's header (core/data/queue.DeadLetterBulkManager).
func (b *fileBroker) PurgeDeadLetters(ctx context.Context, before time.Time, max int) (removed int, err error) {
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
	//: the records that failed strictly before the instant.
	return b.removeDeadFailedBy(ctx, before.UnixNano()-1, max)
}

// sweepRetention removes every dead letter past the policy's
// DeadLetterRetention, at most once a minute per broker on its clock.
func (b *fileBroker) sweepRetention(now int64) error {
	swept := b.deadSwept.Load()
	//: no retention, a sweep less than a minute ago, or another worker of
	//: this broker sweeping now.
	if !retentionDue(b.policy, swept, now) || !b.deadSwept.CompareAndSwap(swept, now) {
		return nil
	}
	_, err := b.removeDeadFailedBy(context.Background(), retentionCutoff(b.policy, now), deadSweepAll)
	//: QueueBackendFailed when the medium refused.
	return err
}

// removeDeadFailedBy removes, in name order and up to max, every dead-letter
// record whose failure instant is at or before cutoff, and counts them. A
// record whose header cannot be read — a stray, a planted file, one still
// being published — is left alone: what it records is unknown.
func (b *fileBroker) removeDeadFailedBy(ctx context.Context, cutoff int64, max int) (removed int, err error) {
	entries, listErr := b.namesOf(dirDead)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return 0, listErr
	}
	//: oldest enqueued first.
	for _, base := range entries {
		//: the caller stopped asking, or the batch is full.
		if ctx.Err() != nil || removed == max {
			return removed, ctx.Err()
		}
		gone, removeErr := b.removeIfFailedBy(base, cutoff)
		//: the medium refused.
		if removeErr != nil {
			//: QueueBackendFailed, with the count so far.
			return removed, removeErr
		}
		//: removed by this call.
		if gone {
			removed++
		}
	}
	//: how many.
	return removed, nil
}

// removeIfFailedBy removes the dead-letter record base when its failure
// instant is known and at or before cutoff, and reports whether this call
// removed it. A record removed meanwhile by somebody else is not this call's
// to count.
func (b *fileBroker) removeIfFailedBy(base string, cutoff int64) (gone bool, err error) {
	failed, known, headErr := b.deadFailedAt(base)
	//: the medium refused.
	if headErr != nil {
		//: QueueBackendFailed.
		return false, headErr
	}
	//: unknown, or failed after the cutoff: kept.
	if !known || failed > cutoff {
		//: not removed.
		return false, nil
	}
	removeErr := b.root.Remove(path.Join(dirDead, base))
	//: removed meanwhile by somebody else, or the medium refused.
	if removeErr != nil {
		//: nil when it was already gone, QueueBackendFailed when it is the medium.
		return false, ignoreMissing("remove", base, removeErr)
	}
	//: removed.
	return true, nil
}

// deadFailedAt reads the failure instant of the dead-letter record base from
// its header alone, never its payload.
func (b *fileBroker) deadFailedAt(base string) (failed int64, known bool, err error) {
	//: the name's own grammar first: a temporary is not a record yet.
	if _, named := parseDead(base); !named {
		//: not ours.
		return 0, false, nil
	}
	file, openErr := b.root.Open(path.Join(dirDead, base))
	//: removed between the listing and the read is not a fault.
	if openErr != nil {
		//: nil for an absent file, QueueBackendFailed otherwise.
		return 0, false, ignoreMissing("open", base, openErr)
	}
	head := make([]byte, deadHeaderMax)
	n, readErr := io.ReadFull(file, head)
	closeErr := file.Close()
	//: a record shorter than the bound is read whole, which is no failure.
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		//: QueueBackendFailed.
		return 0, false, backendFailed("read", base, readErr)
	}
	//: a close the medium refused.
	if closeErr != nil {
		//: QueueBackendFailed.
		return 0, false, backendFailed("close", base, closeErr)
	}
	header, _, split := bytes.Cut(head[:n], []byte(deadHeaderSep))
	//: a header that does not end within the bound is not one this package
	//: wrote.
	if !split {
		//: unknown.
		return 0, false, nil
	}
	record, ok := decodeDead(append(header, deadHeaderSep...))
	//: the magic line and the failure field, or nothing known.
	if !ok || record.FailedAt.IsZero() {
		//: unknown.
		return 0, false, nil
	}
	//: the instant the burial recorded.
	return record.FailedAt.UnixNano(), true, nil
}

// PublishMany publishes every payload in order and wakes once
// (core/data/queue.BatchPublisher). Every name is minted first; then the
// batch goes to vfs's AtomicBatchWriter in one call — each message its own
// atomic publication, its file flushed before its rename, and ready/ flushed
// ONCE after the last rename (issue #827), where a Publish per message paid a
// directory flush each. A publisher without the sibling publishes message by
// message, as before.
//
// On a failure at the k-th payload, the messages returned beside the error —
// the first k−1 — are on the device, and the rest were never written. A
// directory flush that fails leaves the batch visible and its durability in
// doubt: nothing is returned as published then, as Publish returns nothing
// beside the same failure, and a retry may deliver a message twice — which
// at-least-once permits.
func (b *fileBroker) PublishMany(ctx context.Context, payloads [][]byte) ([]corequeue.MessageValue, error) {
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
	batcher, batched := b.publisher.(corevfs.AtomicBatchWriter)
	//: a publisher that cannot share the flush: one publication each.
	if !batched {
		return b.publishEach(payloads, now)
	}
	messages, files, mintErr := mintFileBatch(payloads, now)
	//: no entropy, nothing written.
	if mintErr != nil {
		//: QueueBackendFailed.
		return nil, mintErr
	}
	published, writeErr := batcher.WriteAtomicAll(files, messageMode)
	//: the medium refused part-way.
	if writeErr != nil {
		//: what was renamed is receivable, durable or not.
		b.wakeIfAny(published)
		//: a flush that failed vouches for none of them.
		if kerrs.HasCode(writeErr, corevfs.CodeDirectorySyncFailed) {
			published = 0
		}
		//: QueueBackendFailed, the durable prefix beside it.
		return messages[:published], backendFailed("publish", dirReady, writeErr)
	}
	b.wakeIfAny(published)
	//: one message per payload, every one on the device.
	return messages, nil
}

// mintFileBatch names every payload of a batch as Publish names one, enqueued and
// visible at now, and returns the messages and the files to publish.
func mintFileBatch(payloads [][]byte, now time.Time) ([]corequeue.MessageValue, []corevfs.FileValue, error) {
	messages := make([]corequeue.MessageValue, len(payloads))
	files := make([]corevfs.FileValue, len(payloads))
	//: one identifier each, before a byte is written.
	for index, payload := range payloads {
		entropy, entropyErr := randomHex(idEntropyBytes)
		//: no entropy: nothing is written.
		if entropyErr != nil {
			//: QueueBackendFailed.
			return nil, nil, backendFailed("rand", "", entropyErr)
		}
		name := nameValue{At: now.UnixNano(), EnqueuedAt: now.UnixNano(), Entropy: entropy, Priority: corequeue.PriorityNormal}
		messages[index] = corequeue.MessageValue{ID: name.ID(), Payload: payload, EnqueuedAt: now}
		files[index] = corevfs.FileValue{Name: path.Join(dirReady, name.readyName()), Data: payload}
	}
	//: named, nothing written.
	return messages, files, nil
}

// publishEach publishes the payloads one by one, each through Publish's own
// durable write, and stops at the first failure with what was published.
func (b *fileBroker) publishEach(payloads [][]byte, now time.Time) ([]corequeue.MessageValue, error) {
	messages := make([]corequeue.MessageValue, 0, len(payloads))
	//: in order; the first failure stops the batch.
	for _, payload := range payloads {
		name, writeErr := b.write(payload, now, now, corequeue.PriorityNormal)
		//: the medium refused: what is published is said, the rest is not.
		if writeErr != nil {
			b.wakeIfAny(len(messages))
			return messages, writeErr
		}
		messages = append(messages, corequeue.MessageValue{ID: name.ID(), Payload: payload, EnqueuedAt: now})
	}
	b.wakeIfAny(len(messages))
	//: one message per payload, every one on the device.
	return messages, nil
}

// wakeIfAny wakes the consumers of this queue when n messages were queued.
func (b *fileBroker) wakeIfAny(n int) {
	//: nothing queued wakes nobody.
	if n > 0 {
		b.wake.fire()
	}
}

// AckMany acknowledges every receipt as Ack acknowledges one — an unlink each,
// no flush, as Ack's — and reports one outcome per receipt
// (core/data/queue.Acker). A failure of the medium stops the batch: the
// receipts after it are reported not attempted.
func (b *fileBroker) AckMany(ctx context.Context, receipts []corequeue.ReceiptValue) (corequeue.AckResultValue, error) {
	result := corequeue.AckResultValue{Outcomes: make([]corequeue.AckOutcome, len(receipts))}
	//: in order.
	for index, receipt := range receipts {
		ackErr := b.Ack(ctx, receipt)
		//: acknowledged.
		if ackErr == nil {
			result.Outcomes[index] = corequeue.AckAcknowledged
			continue
		}
		//: the caller's deadline, or the medium: the rest is not attempted.
		if ctx.Err() != nil || !refusedReceipt(ackErr) {
			return result, ackErr
		}
		result.Outcomes[index] = ackOutcomeOf(ackErr)
	}
	//: one outcome per receipt.
	return result, nil
}

// AllMessages yields the live messages in identifier order over one read of
// each state directory, reading each payload only when its message is
// yielded (core/data/queue.MessageLister).
func (b *fileBroker) AllMessages(ctx context.Context, after string) iter.Seq2[corequeue.LiveMessageValue, error] {
	return func(yield func(corequeue.LiveMessageValue, error) bool) {
		//: a cancelled caller.
		if ctx.Err() != nil {
			yieldFailure(yield, ctx.Err())
			return
		}
		found, listErr := b.liveFiles(after)
		//: the medium refused.
		if listErr != nil {
			yieldFailure(yield, listErr)
			return
		}
		//: in identifier order, a payload at a time.
		for _, f := range found {
			live, ok, readErr := b.readLive(ctx, f)
			//: the medium refused, or the caller stopped asking.
			if readErr != nil {
				yieldFailure(yield, readErr)
				return
			}
			//: acknowledged, leased or moved meanwhile.
			if !ok {
				continue
			}
			//: the caller broke out of its loop.
			if !yield(live, nil) {
				return
			}
		}
	}
}

// readLive reads the payload of the live message f names, ok false when it
// moved since the listing.
func (b *fileBroker) readLive(ctx context.Context, f liveFile) (live corequeue.LiveMessageValue, ok bool, err error) {
	//: the caller stopped asking.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.LiveMessageValue{}, false, ctx.Err()
	}
	payload, readErr := b.root.ReadFile(path.Join(f.dir, f.base))
	//: acknowledged, leased or moved meanwhile: not one to list.
	if readErr != nil {
		//: nil for a file gone, QueueBackendFailed otherwise.
		return corequeue.LiveMessageValue{}, false, ignoreMissing("read", f.base, readErr)
	}
	//: what the name and the file record.
	return corequeue.LiveMessageValue{
		VisibleAt:  f.name.AtTime(),
		Message:    corequeue.MessageValue{ID: f.id, Payload: payload, EnqueuedAt: f.name.EnqueuedTime()},
		Deliveries: f.name.Deliveries, Leased: f.leased, Priority: f.name.Priority,
	}, true, nil
}
