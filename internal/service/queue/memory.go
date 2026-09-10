// Package queue — the in-memory broker: the double a consumer's own tests run
// against, and the control the durability measurements are read against.
package queue

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/heap"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// instanceNonceBytes is the entropy in a broker's receipt prefix. It is what
// lets an unknown receipt be told apart from an expired one without keeping a
// record of every receipt ever issued: a receipt carrying THIS broker's nonce
// was minted here and has since lapsed, and one carrying anything else was
// never ours.
const instanceNonceBytes int = 8

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
	// Entries are removed LAZILY: an Ack, a Nack or an Extend drops the
	// record from the map and leaves the heap entry to be popped and
	// discarded when its deadline arrives. That keeps every acknowledgement
	// O(1) and bounds the heap by the leases taken within one visibility
	// timeout rather than by the queue's depth.
	expiry *heap.Heap[leaseExpiry]
	// nonce prefixes every receipt this broker mints.
	nonce string
	// ready holds the queued records, ordered by visibleAt then seq.
	ready []*memRecord
	// dead holds what the broker gave up on, oldest first.
	dead   []corequeue.DeadLetterValue
	policy corequeue.PolicyValue
	// mu is an RWMutex because DeadLetters is a genuine read: an
	// investigation must not serialise against the consumers it is
	// investigating. Everything else takes it exclusively, including Receive
	// — a lease MUTATES, which is why core/queue names the verb Receive and
	// not Peek.
	mu  sync.RWMutex
	seq uint64
}

// NewMemory returns an in-process broker holding its messages in the heap.
//
// It refuses a policy it cannot honour at CONSTRUCTION rather than at first
// use, through the same core/queue.PolicyValue.Validate the file broker runs
// — which is what makes this an honest double for it.
func NewMemory(cfg MemoryConfig) (broker corequeue.Broker, err error) {
	//: the shared guard, so both brokers refuse identical inputs identically.
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
		nonce:    nonce,
		inflight: map[corequeue.ReceiptValue]*memRecord{},
		//: earliest deadline first, so the reclaim scan meets the most
		//: overdue lease and stops at the first one still held.
		expiry: heap.New(func(a, b leaseExpiry) int { return a.at.Compare(b.at) }),
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
	b.seq++
	record := &memRecord{
		//: the caller's slice is copied, so a producer that reuses its buffer
		//: cannot rewrite a message the queue has already accepted.
		payload: slices.Clone(payload), id: memID(now, b.seq),
		enqueuedAt: now, visibleAt: now, seq: b.seq,
	}
	b.insertReady(record)
	//: accepted; in this broker "accepted" means "in a map", which is the
	//: whole difference from NewFile.
	//
	//: the returned Payload is the CALLER'S slice, not a second copy. The
	//: caller already holds those bytes — they just passed them — so cloning
	//: again would have doubled the verb's allocation for no reader: measured
	//: at 131 284 B/op for a 64 KiB payload before, 65 762 B/op after, with
	//: slices.Clone accounting for 99.78 % of the bytes in the profile.
	return corequeue.MessageValue{ID: record.id, Payload: payload, EnqueuedAt: now}, nil
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

// takeEligible removes and returns the oldest visible record, or nil.
func (b *memoryBroker) takeEligible(now time.Time) *memRecord {
	//: the slice is ordered by visibleAt, so the head is the only candidate.
	if len(b.ready) == 0 || b.ready[0].visibleAt.After(now) {
		//: nothing eligible.
		return nil
	}
	head := b.ready[0]
	b.ready = b.ready[1:]
	//: the caller moves it into flight.
	return head
}

// lease moves record into flight and builds the delivery for it.
func (b *memoryBroker) lease(record *memRecord, now time.Time) corequeue.DeliveryValue {
	b.seq++
	record.deliveries++
	record.expiresAt = now.Add(b.policy.VisibilityTimeout)
	record.receipt = b.mintReceipt()
	b.inflight[record.receipt] = record
	b.expiry.Push(leaseExpiry{at: record.expiresAt, receipt: record.receipt})
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
	delete(b.inflight, record.receipt)
	//: gone. This is the only call in the domain that removes a message.
	return nil
}

// Nack hands the message back and reports what the broker decided.
func (b *memoryBroker) Nack(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error,
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
	delete(b.inflight, record.receipt)
	now := b.clk.Now()
	//: the BROKER owns this decision, because the consumer is the thing that
	//: dies and a count it held would be lost by exactly that event.
	if record.deliveries >= b.policy.MaxDeliveries {
		b.bury(record, now, describeCause(cause))
		//: abandoned, with its cause, and never delivered again.
		return corequeue.NackValue{Deliveries: record.deliveries, DeadLettered: true}, nil
	}
	record.visibleAt = now.Add(b.policy.RetryDelay)
	b.insertReady(record)
	//: queued again, eligible at VisibleAt.
	return corequeue.NackValue{Deliveries: record.deliveries, VisibleAt: record.visibleAt}, nil
}

// Extend renews a lease and mints the receipt that replaces it.
func (b *memoryBroker) Extend(
	ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration,
) (lease corequeue.LeaseValue, err error) {
	//: the two readings of a non-positive renewal are opposites.
	if by <= 0 {
		//: QueueMisconfigured, naming the argument.
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
	record.expiresAt = b.clk.Now().Add(by)
	record.receipt = b.mintReceipt()
	b.inflight[record.receipt] = record
	//: the previous heap entry is deliberately left where it is: it will be
	//: popped at its own deadline, find no matching record, and be discarded.
	b.expiry.Push(leaseExpiry{at: record.expiresAt, receipt: record.receipt})
	//: a NEW receipt; the old one names nothing from here on.
	return corequeue.LeaseValue{Receipt: record.receipt, ExpiresAt: record.expiresAt}, nil
}

// DeadLetters returns up to max abandoned messages, oldest first, and removes
// none of them.
func (b *memoryBroker) DeadLetters(
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
	//: a genuine READ, and the only one in this broker: an investigation must
	//: not serialise against the consumers it is investigating.
	b.mu.RLock()
	defer b.mu.RUnlock()
	//: a dead letter is evidence; reading it must not consume it.
	return slices.Clone(b.dead[:min(max, len(b.dead))]), nil
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
		due, pending := b.expiry.Peek()
		//: nothing left, or the earliest deadline is still ahead — which, on
		//: a min-heap, means every remaining one is too. This is the early
		//: stop the map could not provide.
		if !pending || due.at.After(now) {
			//: every lapsed lease has been dealt with.
			return
		}
		//: taken off the heap exactly once, whatever it turns out to be.
		b.expiry.Pop() //nolint:errcheck // Peek already reported the entry is there.
		record, leased := b.inflight[due.receipt]
		//: a stale entry: the lease was acknowledged, nacked or extended, so
		//: this deadline no longer names anything. Extend leaves the old entry
		//: behind on purpose, and comparing the deadlines is how it is
		//: recognised without ever having to search the heap for it.
		if !leased || !record.expiresAt.Equal(due.at) {
			continue
		}
		delete(b.inflight, due.receipt)
		b.recoverExpired(record, now)
	}
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
	b.dead = append(b.dead, corequeue.DeadLetterValue{
		Message: corequeue.MessageValue{
			ID: record.id, Payload: slices.Clone(record.payload), EnqueuedAt: record.enqueuedAt,
		},
		Deliveries: record.deliveries, FailedAt: now,
		Reason: cause.reason, Cause: cause.public, Code: cause.code,
	})
}

// insertReady places record in visibility order, ties broken by seq.
//
// The list is kept ordered at INSERTION rather than sorted at Receive: a
// consumer polls far more often than a producer publishes, so sorting on the
// read path would re-derive on every poll an order the insert already knows.
func (b *memoryBroker) insertReady(record *memRecord) {
	at := len(b.ready)
	//: walk from the end: a fresh publish belongs at the tail, which is the
	//: overwhelmingly common case.
	for at > 0 && laterThan(b.ready[at-1], record) {
		at--
	}
	b.ready = slices.Insert(b.ready, at, record)
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
	//: base 36 keeps it short without needing a second alphabet.
	return strconv.FormatInt(now.UnixNano(), receiptRadix) + "-" +
		strconv.FormatUint(seq, receiptRadix)
}
