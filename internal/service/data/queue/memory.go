package queue

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// instanceNonceBytes is the entropy in a broker's receipt prefix. It is what
// lets an unknown receipt be told apart from an expired one without keeping a
// record of every receipt ever issued: a receipt carrying THIS broker's nonce
// was minted here and has since lapsed, and one carrying anything else was
// never ours.
const instanceNonceBytes int = 8

// memIDHalfWidth is how many base-36 digits the widest uint64 takes: each
// half of a memory identifier is padded to it, so identifiers sort by value.
const memIDHalfWidth int = 13

// memoryBroker is a queue that lives entirely in one process's heap.
//
// # What it is for, and what it is emphatically not
//
// It is the test double. A consumer's own suite gets the port's semantics —
// leasing, redelivery on a lapsed lease, the delivery count, the dead-letter
// path, every refusal — with no directory, no flush and no cleanup, which is
// what makes those tests fast enough to run on every save.
//
// It is NOT a queue in the sense the domain's frontier means. It is one
// process, so it crosses no boundary; it holds nothing on a device, so a
// restart is indistinguishable from a total loss. A caller who reaches for it
// in production has bought the asynchrony and thrown away the durability —
// which is precisely the confusion ADR 0053 drew the frontier to prevent, one
// layer down. Use [NewFile].
//
// Its lease expiry is LAZY: nothing here runs a timer, and an expired lease is
// noticed by the next Receive. That is a deliberate match for the file
// broker, which cannot do anything else — a lease held by a process that has
// since died is only ever noticed by whoever looks next.
type memoryBroker struct {
	clk clock.Clock
	// inflight holds the leased records by their receipt.
	inflight map[corequeue.ReceiptValue]*memRecord
	// expiry orders the leases by DEADLINE so a reclaim scan can stop at the
	// first one that is still held.
	//
	// It exists because the benchmark said so. Without it the reclaim scan
	// ranged over the whole inflight MAP on every Receive — a map has no
	// order, so there was nothing to stop early on — and a queue with a deep
	// in-flight set paid for all of it on every poll: 105 µs per lease against
	// the 1.83 µs it costs now, for a verb that is a few hundred nanoseconds
	// of actual work. The file broker never had the problem, because its
	// in-flight directory sorts by deadline and its scan breaks. See BENCH.md.
	//
	// Each entry is named by the Handle its record keeps (memRecord.lapse):
	// an Extend re-keys it in place and an Ack, a Nack, a Reject or a
	// Release removes it, each in O(log n), so the heap holds exactly the
	// leases in flight and every entry it pops is live (#1036). Before, the
	// entries were removed lazily — an acknowledgement left its entry to be
	// discarded when popped, and every Extend left one more behind.
	expiry *heap.IndexedHeap[leaseExpiry]
	// once holds each PublishOnce key still reserved, by the end of its
	// window; onceEnds orders those ends so a passed one is dropped at the
	// next PublishOnce without a scan (core/data/queue.OncePublisher).
	once     map[string]time.Time
	onceEnds *heap.Heap[onceEnd]
	// wake is what an idle consumer of this broker waits on: closed by a
	// Publish and by a Nack that hands a message back.
	wake *wakeSignal
	// nonce prefixes every receipt this broker mints.
	nonce string
	// ready holds the queued records, one list per level — the lowest at
	// index 0, readyIndex — each ordered by visibleAt then seq (ADR 0054,
	// amended 2026-11-03).
	ready [priorityLevels][]*memRecord
	// dead holds what the broker gave up on, in the byte order of their
	// identifiers — the order DeadLetters and DeadLettersAfter list, the
	// durable brokers' (ADR 0054, amended 2026-11-02) —, kept at insertion.
	dead []corequeue.DeadLetterValue
	// random draws the retry jitter (MemoryConfig.Random).
	random func() float64
	// swept is when the last dead-letter retention sweep ran, in Unix
	// nanoseconds; zero for never.
	swept  int64
	policy corequeue.PolicyValue
	// mu is an RWMutex because DeadLetters is a genuine read: an
	// investigation must not serialise against the consumers it is
	// investigating. Everything else takes it exclusively, including Receive
	// — a lease MUTATES, which is why core/data/queue names the verb Receive and
	// not Peek.
	mu  sync.RWMutex
	seq uint64
}

// NewMemory returns an in-process broker holding its messages in the heap.
//
// It refuses a policy it cannot honour at CONSTRUCTION rather than at first
// use, through the same core/data/queue.PolicyValue.Validate the file broker runs
// — which is what makes this an honest double for it.
func NewMemory(cfg MemoryConfig) (broker corequeue.Broker, err error) {
	//: the shared guard, so every broker refuses identical inputs identically.
	if invalid := cfg.Policy.Validate(); invalid != nil {
		//: QueueMisconfigured, naming the field.
		return nil, invalid
	}
	nonce, nonceErr := randomHex(instanceNonceBytes)
	//: an entropy source that fails is not worked around with a counter.
	if nonceErr != nil {
		//: QueueBackendFailed — nothing was constructed.
		return nil, backendFailed("rand", "", nonceErr)
	}
	//: usable.
	return &memoryBroker{
		policy:   cfg.Policy.Normalized(),
		clk:      clockOrSystem(cfg.Clock),
		random:   randomOrDefault(cfg.Random),
		nonce:    nonce,
		wake:     newWakeSignal(),
		inflight: map[corequeue.ReceiptValue]*memRecord{},
		//: earliest deadline first, so the reclaim scan meets the most
		//: overdue lease and stops at the first one still held.
		expiry: newLeaseExpiries(),
		//: the earliest window's end first, for the same early stop.
		once:     map[string]time.Time{},
		onceEnds: heap.New(func(a, b onceEnd) int { return a.at.Compare(b.at) }),
	}, nil
}

// Publish appends payload and returns the message the broker minted.
func (b *memoryBroker) Publish(
	ctx context.Context, payload []byte,
) (message corequeue.MessageValue, err error) {
	//: a cancelled producer gets its own error rather than a message nobody
	//: asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline, reported as the caller's error.
		return corequeue.MessageValue{}, ctx.Err()
	}
	//: the bound is enforced at the producer, where the payload can still be
	//: made smaller.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, tooLarge
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	//: accepted; in this broker "accepted" means "in a map", which is the
	//: whole difference from NewFile.
	return b.enqueue(payload, now, now, corequeue.PriorityNormal), nil
}

// enqueue queues payload at level, enqueued at now and visible at visibleAt,
// wakes every idle consumer, and returns the message it minted. The caller
// holds the write lock and has checked the payload's size.
func (b *memoryBroker) enqueue(payload []byte, now, visibleAt time.Time, level corequeue.Priority) corequeue.MessageValue {
	message := b.enqueueQuiet(payload, now, visibleAt, level)
	//: every idle consumer of this broker looks again now, rather than at its
	//: next poll — and one whose message lies ahead learns when.
	b.wake.fire()
	//: the message as minted.
	return message
}

// enqueueQuiet is enqueue without the wake, for a batch that wakes once. The
// caller holds mu.
func (b *memoryBroker) enqueueQuiet(payload []byte, now, visibleAt time.Time, level corequeue.Priority) corequeue.MessageValue {
	b.seq++
	record := &memRecord{
		//: the caller's slice is copied, so a producer that reuses its buffer
		//: cannot rewrite a message the queue has already accepted.
		payload: slices.Clone(payload), id: memID(now, b.seq),
		enqueuedAt: now, visibleAt: visibleAt, seq: b.seq, priority: level,
	}
	b.insertReady(record)
	//: the returned Payload is the CALLER'S slice, not a second copy. The
	//: caller already holds those bytes — they just passed them — so cloning
	//: again would have doubled the verb's allocation for no reader: measured
	//: at 131 284 B/op for a 64 KiB payload before, 65 762 B/op after, with
	//: slices.Clone accounting for 99.78 % of the bytes in the profile.
	return corequeue.MessageValue{ID: record.id, Payload: payload, EnqueuedAt: now}
}

// PublishPriority queues payload at level (core/data/queue.PriorityPublisher):
// a record in that level's ready list, which a Receive empties of its visible
// records before it looks at a lower level.
func (b *memoryBroker) PublishPriority(
	ctx context.Context, payload []byte, level corequeue.Priority,
) (message corequeue.MessageValue, err error) {
	//: a cancelled producer.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, ctx.Err()
	}
	//: one of the five levels.
	if invalid := checkPriority(level); invalid != nil {
		//: QueueMisconfigured, and nothing is queued.
		return corequeue.MessageValue{}, invalid
	}
	//: the bound Publish enforces.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, tooLarge
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	//: accepted at its level, visible now.
	return b.enqueue(payload, now, now, level), nil
}

// PublishAt queues payload, received by no consumer before at
// (core/data/queue.DelayedPublisher): a record whose visibility is at, in the
// ready list's visibility order.
func (b *memoryBroker) PublishAt(
	ctx context.Context, payload []byte, at time.Time,
) (message corequeue.MessageValue, err error) {
	//: a cancelled producer.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, ctx.Err()
	}
	//: the bound Publish enforces.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, tooLarge
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	visibleAt, invalid := publishVisibleAt(now, at)
	//: an instant the durable brokers could not record.
	if invalid != nil {
		//: QueueMisconfigured, and nothing is queued.
		return corequeue.MessageValue{}, invalid
	}
	//: queued, held back until visibleAt.
	return b.enqueue(payload, now, visibleAt, corequeue.PriorityNormal), nil
}

// PublishOnce queues payload unless key is still reserved, and reserves it
// for window (core/data/queue.OncePublisher). The reservations are a map from
// key to the end of its window, and a heap of those ends drops the passed ones
// at the next PublishOnce — the lazy purge the durable brokers make too.
func (b *memoryBroker) PublishOnce(
	ctx context.Context, key string, payload []byte, window time.Duration,
) (message corequeue.MessageValue, published bool, err error) {
	//: a cancelled producer.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, false, ctx.Err()
	}
	//: the bound Publish enforces.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, false, tooLarge
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	until, invalid := onceUntil(now, key, window)
	//: a key or a window no broker honours.
	if invalid != nil {
		//: QueueMisconfigured, and nothing is queued.
		return corequeue.MessageValue{}, false, invalid
	}
	b.dropPassedOnce(now)
	//: an earlier publication's window still holds the key.
	if held, reserved := b.once[key]; reserved && held.After(now) {
		//: a duplicate: nothing is queued, and that is the answer.
		return corequeue.MessageValue{}, false, nil
	}
	b.once[key] = until
	b.onceEnds.Push(onceEnd{at: until, key: key})
	//: queued, and the key reserved until the window ends.
	return b.enqueue(payload, now, now, corequeue.PriorityNormal), true, nil
}

// dropPassedOnce removes every reservation whose window has ended by now. The
// caller holds the write lock.
func (b *memoryBroker) dropPassedOnce(now time.Time) {
	//: until the heap is empty or its earliest end is still ahead.
	for {
		end, pending := b.onceEnds.Peek()
		//: nothing left, or every remaining window still runs.
		if !pending || end.at.After(now) {
			//: every passed reservation is gone.
			return
		}
		b.onceEnds.Pop() //nolint:errcheck // Peek already reported the entry is there.
		//: a key reserved again since names a later end; that entry is stale.
		if held, reserved := b.once[end.key]; reserved && held.Equal(end.at) {
			delete(b.once, end.key)
		}
	}
}

// Receive leases up to max messages.
func (b *memoryBroker) Receive(
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
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	//: BEFORE anything is handed out: a lease whose holder died is only ever
	//: noticed by whoever looks next, and this is that look.
	b.reclaimExpired(now)
	//: a dead letter past its retention goes on the same look, at most once
	//: a minute.
	b.sweepRetention(now)
	//: nil until something is eligible, so an empty poll allocates nothing.
	var out []corequeue.DeliveryValue
	//: until the batch is full or nothing else is visible.
	for len(out) < max {
		record := b.takeEligible(now)
		//: nothing visible; an empty queue is not an error.
		if record == nil {
			break
		}
		out = append(out, b.lease(record, now))
	}
	//: whatever was visible, oldest first.
	return out, nil
}

// takeEligible removes and returns the oldest visible record of the highest
// level that has one, or nil: a level whose head is not yet visible holds
// back nobody below it.
func (b *memoryBroker) takeEligible(now time.Time) *memRecord {
	//: the highest level first — strict, with no ageing.
	for level := priorityLevels - 1; level >= 0; level-- {
		//: this level's oldest visible record, if any.
		if head := b.takeVisible(level, now); head != nil {
			//: the caller moves it into flight.
			return head
		}
	}
	//: nothing eligible at any level.
	return nil
}

// takeVisible removes and returns the head of one level's ready list when it
// is visible, burying first every visible head past its MaxAge.
func (b *memoryBroker) takeVisible(level int, now time.Time) *memRecord {
	list := b.ready[level]
	//: a visible head past its MaxAge is never delivered: it is buried, and
	//: the next one is looked at.
	for len(list) > 0 && !list[0].visibleAt.After(now) && b.expired(list[0].enqueuedAt, now) {
		b.bury(list[0], now, expiredCause())
		list = list[1:]
	}
	b.ready[level] = list
	//: the list is ordered by visibleAt, so the head is the only candidate.
	if len(list) == 0 || list[0].visibleAt.After(now) {
		//: nothing eligible at this level.
		return nil
	}
	b.ready[level] = list[1:]
	//: the oldest visible record of the level.
	return list[0]
}

// lease moves record into flight and builds the delivery for it.
func (b *memoryBroker) lease(record *memRecord, now time.Time) corequeue.DeliveryValue {
	b.seq++
	record.deliveries++
	record.expiresAt = now.Add(b.policy.VisibilityTimeout)
	record.receipt = b.mintReceipt()
	b.inflight[record.receipt] = record
	record.lapse = b.expiry.Push(leaseExpiry{at: record.expiresAt, receipt: record.receipt})
	//: the payload is copied out, so a handler may keep or mutate it without
	//: corrupting the redelivery this queue is allowed to make.
	return corequeue.DeliveryValue{
		Message: corequeue.MessageValue{
			ID: record.id, Payload: slices.Clone(record.payload), EnqueuedAt: record.enqueuedAt,
		},
		Lease:      corequeue.LeaseValue{Receipt: record.receipt, ExpiresAt: record.expiresAt},
		Deliveries: record.deliveries,
	}
}

// mintReceipt returns a receipt unique to this broker and this lease. The
// caller holds the write lock and has already advanced seq.
func (b *memoryBroker) mintReceipt() corequeue.ReceiptValue {
	//: the nonce is the provenance half, the counter the uniqueness half.
	return corequeue.ReceiptValue(b.nonce + "-" + strconv.FormatUint(b.seq, receiptRadix))
}

// Ack removes the leased message permanently.
func (b *memoryBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	//: a cancelled caller must not be told its work is safely acknowledged.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, resolveErr := b.resolve(receipt)
	//: an unknown receipt and a lapsed one are different bugs.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return resolveErr
	}
	b.unlease(record)
	//: gone. This is the only call in the domain that removes a message.
	return nil
}

// Nack hands the message back and reports what the broker decided.
func (b *memoryBroker) Nack(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error,
) (verdict corequeue.NackValue, err error) {
	//: the policy's delay, grown with the deliveries when it asks for that.
	return b.nackWith(ctx, receipt, cause, func(deliveries int) time.Duration {
		//: the curve every broker waits on.
		return retryDelay(b.policy, deliveries, b.random)
	})
}

// NackAfter hands the message back as Nack does, receivable delay after now
// instead of after the policy's retry delay (core/data/queue.DelayedNacker).
func (b *memoryBroker) NackAfter(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration,
) (verdict corequeue.NackValue, err error) {
	//: an instant the durable brokers could not record is refused here too:
	//: a double more permissive than production lets a handler pass its tests
	//: and strand messages.
	if _, invalid := nackDeadline(b.clk.Now(), delay); invalid != nil {
		//: QueueMisconfigured, naming the argument; the lease stays held.
		return corequeue.NackValue{}, invalid
	}
	//: the caller's delay, whatever the delivery.
	return b.nackWith(ctx, receipt, cause, func(int) time.Duration {
		//: a negative delay is now.
		return max(delay, 0)
	})
}

// nackWith is Nack and NackAfter: the message handed back after wait, or
// dead-lettered with cause when it has no attempt left.
func (b *memoryBroker) nackWith(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error, wait func(deliveries int) time.Duration,
) (verdict corequeue.NackValue, err error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.NackValue{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, resolveErr := b.resolve(receipt)
	//: same refusal as Ack, for the same reason.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return corequeue.NackValue{}, resolveErr
	}
	b.unlease(record)
	now := b.clk.Now()
	//: the BROKER owns this decision, because the consumer is the thing that
	//: dies and a count it held would be lost by exactly that event.
	if record.deliveries >= b.policy.MaxDeliveries {
		b.bury(record, now, describeCause(cause))
		//: abandoned, with its cause, and never delivered again.
		return corequeue.NackValue{Deliveries: record.deliveries, DeadLettered: true}, nil
	}
	record.visibleAt = now.Add(wait(record.deliveries))
	b.insertReady(record)
	//: an idle consumer re-reads when to look, which now includes VisibleAt.
	b.wake.fire()
	//: queued again, eligible at VisibleAt.
	return corequeue.NackValue{Deliveries: record.deliveries, VisibleAt: record.visibleAt}, nil
}

// Reject dead-letters the leased message at once with cause, whatever its
// delivery count (core/data/queue.Rejecter).
func (b *memoryBroker) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, resolveErr := b.resolve(receipt)
	//: the same refusals Ack makes, and nothing moves.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return resolveErr
	}
	b.unlease(record)
	b.bury(record, b.clk.Now(), describeCause(cause))
	//: abandoned with the handler's cause, at the count it had.
	return nil
}

// Release hands the leased message back, visible now, with the delivery its
// lease counted given back (core/data/queue.Releaser).
func (b *memoryBroker) Release(ctx context.Context, receipt corequeue.ReceiptValue) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, resolveErr := b.resolve(receipt)
	//: the same refusals Ack makes, and nothing moves.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return resolveErr
	}
	b.unlease(record)
	//: this lease never became an attempt, so it is not one.
	record.deliveries = max(record.deliveries-1, 0)
	record.visibleAt = b.clk.Now()
	b.insertReady(record)
	//: an idle consumer may take it now.
	b.wake.fire()
	//: queued again, receivable at once.
	return nil
}

// ReplayDeadLetter queues the dead letter id again, visible now, with its
// delivery count reset (core/data/queue.DeadLetterManager). It keeps its ID, its
// payload and its enqueue instant.
func (b *memoryBroker) ReplayDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dead, found := b.takeDead(id)
	//: nothing under that identifier.
	if !found {
		//: DeadLetterNotFound, and nothing is queued.
		return deadLetterNotFound("memory", id)
	}
	//: the payload the store kept is its own copy, so it moves as it is.
	b.requeueDead(dead, b.clk.Now())
	//: an idle consumer looks again now.
	b.wake.fire()
	//: queued; its next delivery counts 1.
	return nil
}

// DeleteDeadLetter removes the dead letter id for good
// (core/data/queue.DeadLetterManager).
func (b *memoryBroker) DeleteDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	//: nothing under that identifier.
	if _, found := b.takeDead(id); !found {
		//: DeadLetterNotFound, and nothing is removed.
		return deadLetterNotFound("memory", id)
	}
	//: gone, payload, cause and all.
	return nil
}

// PutDeadLetter appends dead to the dead-letter store as it is given, under an
// identifier minted from its enqueue instant and this broker's sequence
// (core/data/queue.DeadLetterWriter). The payload is copied, so a caller that
// reuses its buffer cannot rewrite the record.
func (b *memoryBroker) PutDeadLetter(ctx context.Context, dead corequeue.DeadLetterValue) (string, error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return "", ctx.Err()
	}
	//: the refusals every broker makes, through the same guard.
	if invalid := checkDeadLetter(dead, b.policy.MaxMessageBytes); invalid != nil {
		//: InvalidDeadLetter or MessageTooLarge.
		return "", invalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	dead.Message.ID = memID(dead.Message.EnqueuedAt, b.seq)
	dead.Message.Payload = slices.Clone(dead.Message.Payload)
	dead.Trail = cloneTrail(dead.Trail)
	b.insertDead(dead)
	//: dead, as it was given, after every record the store already holds.
	return dead.Message.ID, nil
}

// Messages lists up to max live messages — queued and leased — whose
// identifier sorts after after, in the byte order of their identifiers
// (core/data/queue.MessageReader), each payload a copy. It is a read: it
// leases nothing and reclaims no lapsed lease.
func (b *memoryBroker) Messages(ctx context.Context, after string, max int) ([]corequeue.LiveMessageValue, error) {
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	//: the queued ones, then the leased ones, after where the listing
	//: resumes; their payloads are copied only for the page handed out.
	found := b.readyAfter(after)
	//: a map has no order; the sort below gives one.
	for _, record := range b.inflight {
		//: before where the listing resumes.
		if record.id > after {
			found = append(found, record)
		}
	}
	slices.SortFunc(found, func(x, y *memRecord) int {
		//: byte order of the identifiers, as the durable brokers list them.
		return strings.Compare(x.id, y.id)
	})
	var live []corequeue.LiveMessageValue
	//: a bounded batch: a queued one visible at its instant, a leased one
	//: back at its lease's deadline.
	for _, record := range found[:min(max, len(found))] {
		_, leased := b.inflight[record.receipt]
		visibleAt := record.visibleAt
		//: a leased record's instant is its deadline.
		if leased && record.receipt != "" {
			visibleAt = record.expiresAt
		}
		live = append(live, liveOf(record, visibleAt, leased && record.receipt != ""))
	}
	//: the page.
	return live, nil
}

// liveOf is record as MessageReader lists it, visible at visibleAt, its
// payload a copy the caller owns.
func liveOf(record *memRecord, visibleAt time.Time, leased bool) corequeue.LiveMessageValue {
	//: what the broker records of it.
	return corequeue.LiveMessageValue{
		VisibleAt:  visibleAt,
		Message:    corequeue.MessageValue{ID: record.id, Payload: slices.Clone(record.payload), EnqueuedAt: record.enqueuedAt},
		Deliveries: record.deliveries, Leased: leased, Priority: record.priority,
	}
}

// PutMessage queues live as it is given, under an identifier minted from its
// enqueue instant and this broker's sequence (core/data/queue.MessageWriter):
// visible at its instant, with its count, its payload copied. An idle
// consumer is woken.
func (b *memoryBroker) PutMessage(ctx context.Context, live corequeue.LiveMessageValue) (string, error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return "", ctx.Err()
	}
	//: the refusals every broker makes, through the same guard.
	if invalid := checkLiveMessage(live, b.policy.MaxMessageBytes); invalid != nil {
		//: InvalidMessage or MessageTooLarge.
		return "", invalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	record := &memRecord{
		payload: slices.Clone(live.Message.Payload), id: memID(live.Message.EnqueuedAt, b.seq),
		enqueuedAt: live.Message.EnqueuedAt, visibleAt: live.VisibleAt, seq: b.seq, deliveries: live.Deliveries,
		priority: live.Priority,
	}
	b.insertReady(record)
	b.wake.fire()
	//: queued, as it was given, under its new identifier.
	return record.id, nil
}

// takeDead removes and returns the dead letter id. The caller holds the write
// lock.
func (b *memoryBroker) takeDead(id string) (dead corequeue.DeadLetterValue, found bool) {
	at := slices.IndexFunc(b.dead, func(record corequeue.DeadLetterValue) bool { return record.Message.ID == id })
	//: never dead, or already replayed or deleted.
	if at < 0 {
		//: not found.
		return corequeue.DeadLetterValue{}, false
	}
	dead = b.dead[at]
	b.dead = slices.Delete(b.dead, at, at+1)
	//: the record, out of the store.
	return dead, true
}

// Extend renews a lease and mints the receipt that replaces it.
func (b *memoryBroker) Extend(
	ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration,
) (lease corequeue.LeaseValue, err error) {
	//: one reading of the clock, as the file broker takes: a deadline checked
	//: at one instant and stored from another could be one the check refused.
	deadline := b.clk.Now().Add(by)
	//: the two readings of a non-positive renewal are opposites. And a by
	//: whose deadline the durable broker cannot write into a name is refused
	//: too, in the same order: time.Time would hold it, but this broker is
	//: that one's double, and a double more permissive than production lets a
	//: handler pass its tests and strand messages.
	if by <= 0 || !nameable(deadline) {
		//: QueueMisconfigured, naming the argument, exactly as the file broker.
		return corequeue.LeaseValue{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "Extend.by"), kerrs.Int64("value_ns", int64(by)))
	}
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.LeaseValue{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, resolveErr := b.resolve(receipt)
	//: a lapsed lease is NOT renewed, even when nobody has taken the message.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return corequeue.LeaseValue{}, resolveErr
	}
	delete(b.inflight, record.receipt)
	b.seq++
	record.expiresAt = deadline
	record.receipt = b.mintReceipt()
	b.inflight[record.receipt] = record
	//: the lease's own entry, re-keyed in place: the heap keeps one entry per
	//: lease however often it is extended.
	b.expiry.Update(record.lapse, leaseExpiry{at: record.expiresAt, receipt: record.receipt})
	//: a NEW receipt; the old one names nothing from here on.
	return corequeue.LeaseValue{Receipt: record.receipt, ExpiresAt: record.expiresAt}, nil
}

// Wake reports what an idle consumer should wait on: the signal the next
// Publish or Nack closes, and how long until the earliest message this broker
// holds becomes receivable on its own — the head of the ready list, whose
// retry delay is ending, or the earliest lease, whose holder may have died.
//
// Both are read from the structures Receive itself uses, under the same lock,
// so the answer is never staler than the call. The expiry heap holds exactly
// the leases in flight, so its top is a lease that can still lapse.
func (b *memoryBroker) Wake() corequeue.WakeValue {
	b.mu.RLock()
	defer b.mu.RUnlock()
	signal, _ := b.wake.current()
	due := b.earliestReady()
	//: a min-heap's top is the earliest lease deadline.
	if lapse, _, held := b.expiry.Peek(); held && (due.IsZero() || lapse.at.Before(due)) {
		due = lapse.at
	}
	//: measured on this broker's clock, now.
	return wakeValue(signal, due, b.clk.Now())
}

// DeadLetters returns up to max abandoned messages, oldest enqueued first,
// and removes none of them: DeadLettersAfter's first page.
func (b *memoryBroker) DeadLetters(
	ctx context.Context, max int,
) (dead []corequeue.DeadLetterValue, err error) {
	//: the first page, from the first identifier.
	return b.DeadLettersAfter(ctx, "", max)
}

// resolve returns the record a receipt names, or the reason it names none.
//
// The nonce is what makes the two refusals distinguishable without keeping a
// record of every receipt ever issued: one carrying THIS broker's prefix was
// minted here and has since lapsed, and one carrying anything else — a forged
// string, a receipt from another queue, one held across a restart — was never
// ours. "You were too slow" and "this was never yours" are different bugs.
func (b *memoryBroker) resolve(receipt corequeue.ReceiptValue) (record *memRecord, err error) {
	held, leased := b.inflight[receipt]
	//: held AND still within its deadline. Expiry is checked here and not
	//: only in reclaimExpired, because a lapsed lease must be refused the
	//: instant it lapses rather than at whatever later moment somebody
	//: happens to call Receive — otherwise "still mine?" would have two
	//: different answers depending on who else was polling.
	if leased && held.expiresAt.After(b.clk.Now()) {
		//: still leased by this caller.
		return held, nil
	}
	//: never minted here.
	if !strings.HasPrefix(string(receipt), b.nonce+"-") {
		//: UnknownReceipt.
		return nil, kerrs.Wrap(corequeue.UnknownReceipt, kerrs.WrapParams{},
			kerrs.String("broker", "memory"))
	}
	//: ours, and gone: the visibility timeout elapsed.
	return nil, kerrs.Wrap(corequeue.LeaseExpired, kerrs.WrapParams{},
		kerrs.String("broker", "memory"))
}

// reclaimExpired returns every lapsed lease to the queue, or to the
// dead-letter store when it has no attempts left.
//
// A message whose consumer died is dead-lettered by the SAME count comparison
// a nack uses: a process that crashes on every attempt must eventually stop
// being retried, or the poison message that killed it loops forever.
func (b *memoryBroker) reclaimExpired(now time.Time) {
	//: until the heap is empty or its earliest deadline is in the future.
	for {
		due, _, pending := b.expiry.Peek()
		//: nothing left, or the earliest deadline is still ahead — which, on
		//: a min-heap, means every remaining one is too. This is the early
		//: stop the map could not provide.
		if !pending || due.at.After(now) {
			//: every lapsed lease has been dealt with.
			return
		}
		//: taken off the heap exactly once; the entry is the lease's own, so
		//: its handle names nothing from here on.
		b.expiry.Pop() //nolint:errcheck // Peek already reported the entry is there.
		record, leased := b.inflight[due.receipt]
		//: unreachable: every call that ends a lease removes its entry, so a
		//: popped entry names a lease in flight. Kept so a broken invariant
		//: loses a reclaim rather than the process.
		if !leased {
			continue
		}
		record.lapse = heap.Handle{}
		delete(b.inflight, due.receipt)
		b.recoverExpired(record, now)
	}
}

// unlease takes record out of flight: out of the in-flight map, and its
// deadline out of the expiry heap. The caller holds the write lock.
func (b *memoryBroker) unlease(record *memRecord) {
	delete(b.inflight, record.receipt)
	//: the lease's own entry, wherever it sits in the heap.
	b.expiry.Remove(record.lapse) //nolint:errcheck // a record in flight always holds a live handle.
	record.lapse = heap.Handle{}
}

// newLeaseExpiries returns an empty expiry heap, earliest deadline first.
func newLeaseExpiries() *heap.IndexedHeap[leaseExpiry] {
	//: time.Time's own order.
	return heap.NewIndexed(func(a, b leaseExpiry) int { return a.at.Compare(b.at) })
}

// recoverExpired decides what becomes of one lapsed lease.
func (b *memoryBroker) recoverExpired(record *memRecord, now time.Time) {
	//: out of attempts: nobody ever reported why, so the reason is the expiry
	//: itself — the signature of a consumer that took the process down with
	//: it, since a process that dies cannot nack.
	if record.deliveries >= b.policy.MaxDeliveries {
		b.bury(record, now, causeValue{reason: reasonLeaseExpired})
		//: abandoned; it is never delivered again.
		return
	}
	//: a lapsed lease has ALREADY waited a whole visibility timeout, so it is
	//: not charged the retry delay on top: a crashed consumer must not be
	//: punished harder than a failing one.
	record.visibleAt = now
	b.insertReady(record)
}

// bury moves a record into the dead-letter store with its cause.
func (b *memoryBroker) bury(record *memRecord, now time.Time, cause causeValue) {
	b.insertDead(corequeue.DeadLetterValue{
		Message: corequeue.MessageValue{
			ID: record.id, Payload: slices.Clone(record.payload), EnqueuedAt: record.enqueuedAt,
		},
		Deliveries: record.deliveries, FailedAt: now,
		Reason: cause.reason, Cause: cause.public, Code: cause.code, Trail: cause.trail,
		Priority: record.priority,
	})
}

// insertDead keeps the dead list in the byte order of the identifiers, so the
// memory broker lists its dead letters as the durable ones do — by enqueue
// instant, whatever order they died in — and a cursor resumes by a binary
// search.
func (b *memoryBroker) insertDead(dead corequeue.DeadLetterValue) {
	at, _ := slices.BinarySearchFunc(b.dead, dead.Message.ID, compareDeadID)
	b.dead = slices.Insert(b.dead, at, dead)
}

// compareDeadID orders a dead letter against an identifier, by bytes.
func compareDeadID(held corequeue.DeadLetterValue, id string) int {
	//: the order every broker lists its dead letters in.
	return strings.Compare(held.Message.ID, id)
}

// insertReady places record in its level's list, in visibility order, ties
// broken by seq.
//
// The list is kept ordered at INSERTION rather than sorted at Receive: a
// consumer polls far more often than a producer publishes, so sorting on the
// read path would re-derive on every poll an order the insert already knows.
func (b *memoryBroker) insertReady(record *memRecord) {
	level := readyIndex(record.priority)
	list := b.ready[level]
	at := len(list)
	//: walk from the end: a fresh publish belongs at the tail, which is the
	//: overwhelmingly common case.
	for at > 0 && laterThan(list[at-1], record) {
		at--
	}
	b.ready[level] = slices.Insert(list, at, record)
}

// earliestReady is the earliest instant a queued record becomes visible: the
// earliest head of the levels' lists, each in visibility order; the zero
// time.Time when nothing waits.
func (b *memoryBroker) earliestReady() time.Time {
	var due time.Time
	//: every level's head.
	for _, list := range b.ready {
		//: the earliest of them.
		if len(list) > 0 && (due.IsZero() || list[0].visibleAt.Before(due)) {
			due = list[0].visibleAt
		}
	}
	//: zero when every list is empty.
	return due
}

// readyAfter is every queued record, every level's, whose identifier sorts
// after after, in no order.
func (b *memoryBroker) readyAfter(after string) []*memRecord {
	var found []*memRecord
	//: every level's list.
	for _, list := range b.ready {
		//: after where the listing resumes.
		for _, record := range list {
			//: before it is not listed.
			if record.id > after {
				found = append(found, record)
			}
		}
	}
	//: possibly none.
	return found
}

// readyCount is how many records wait, every level together.
func (b *memoryBroker) readyCount() int {
	count := 0
	//: every level.
	for _, list := range b.ready {
		count += len(list)
	}
	//: the total.
	return count
}

// laterThan reports whether first sorts after second in the ready order.
func laterThan(first, second *memRecord) bool {
	//: equal instants are ordered by publication sequence, so two messages
	//: published in the same nanosecond still come out in the order they
	//: went in.
	if first.visibleAt.Equal(second.visibleAt) {
		//: the later publication sorts later.
		return first.seq > second.seq
	}
	//: visibility first.
	return first.visibleAt.After(second.visibleAt)
}

// memID mints an opaque identifier. It is the enqueue instant and the
// broker's sequence, which is unique within one broker and sorts the way the
// queue does — but it is documented as opaque, and nothing outside this file
// reads it back.
func memID(now time.Time, seq uint64) string {
	//: base 36 keeps it short without needing a second alphabet; each half
	//: is zero-padded to its widest, so the byte order of two identifiers is
	//: the order of their instants, then of their sequence — what a dead
	//: letter's place in the list and a cursor compare (issue #1043). Built
	//: in one buffer, so the identifier is the verb's one allocation.
	var id [2*memIDHalfWidth + 1]byte
	padBase36(id[:memIDHalfWidth], uint64(max(now.UnixNano(), 0)))
	id[memIDHalfWidth] = '-'
	padBase36(id[memIDHalfWidth+1:], seq)
	return string(id[:])
}

// padBase36 writes n in base 36 into into, right-aligned and zero-padded;
// into is memIDHalfWidth long, the widest a uint64 takes.
func padBase36(into []byte, n uint64) {
	var scratch [memIDHalfWidth]byte
	digits := strconv.AppendUint(scratch[:0], n, receiptRadix)
	pad := len(into) - len(digits)
	//: the leading zeros, then the digits.
	for index := range pad {
		into[index] = '0'
	}
	copy(into[pad:], digits)
}
