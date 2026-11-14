package queue

import (
	"context"
	"os"
	"path"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// Receive reclaims lapsed leases, then leases up to max visible messages.
//
// Reclaiming FIRST is the whole recovery mechanism and it is deliberately not
// a background task. There is no sweeper goroutine, no timer and no daemon:
// a lease held by a process that has since been killed is noticed by whoever
// looks next, which is exactly this call, in whatever process makes it. A
// sweeper would be a fourth thing that can die, and it would have to be
// elected between processes to avoid all of them sweeping at once.
func (b *fileBroker) Receive(
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
	lapse, reclaimErr := b.reclaim(now)
	//: BEFORE anything is handed out.
	if reclaimErr != nil {
		//: QueueBackendFailed — the medium refused mid-recovery.
		return nil, reclaimErr
	}
	//: a dead letter past its retention goes on the same look, at most once
	//: a minute.
	if sweepErr := b.sweepRetention(now); sweepErr != nil {
		//: QueueBackendFailed.
		return nil, sweepErr
	}
	batch, visible, leaseErr := b.leaseVisible(now, max)
	//: the medium refused while leasing.
	if leaseErr != nil {
		//: QueueBackendFailed.
		return nil, leaseErr
	}
	var leased int64
	//: a lease taken now lapses at now + VisibilityTimeout if its holder
	//: dies, and that is when an idle consumer should look again.
	if len(batch) > 0 {
		leased = now + int64(b.policy.VisibilityTimeout)
	}
	//: both scans stopped at the first future instant, which is exactly the
	//: earliest moment anything changes on its own.
	b.wake.record(earliest(lapse, visible, leased))
	//: whatever is visible, oldest first.
	return batch, nil
}

// leaseVisible walks the queued messages level by level, highest first, each
// level in visibility order, and leases up to max of them. A level's first
// message not yet visible ends that level's walk, never the next level's: a
// high message held back does not hold back a visible normal one (ADR 0054,
// amended 2026-11-03). It also returns the instant, in Unix nanoseconds, at
// which the next queued message becomes visible — now itself when it stopped
// on a full batch with more behind it, zero when nothing else is queued.
//
// The walk reads the broker's index of ready/, brought up to the directory
// first (file_index.go): a heap a level, so a Receive costs the same over ten
// queued messages as over a million (issue #811).
func (b *fileBroker) leaseVisible(
	now int64, max int,
) (batch []corequeue.DeliveryValue, next int64, err error) {
	//: the queued changes applied, or the directory read again.
	if syncErr := b.index.sync(b, dirReady); syncErr != nil {
		//: QueueBackendFailed.
		return nil, 0, syncErr
	}
	walk := leaseWalk{now: now, max: max}
	//: by level, highest first: the order of the digits.
	for level := range readyLevels {
		full, stepErr := b.leaseLevel(&walk, level)
		//: the medium refused.
		if stepErr != nil {
			//: QueueBackendFailed.
			return nil, 0, stepErr
		}
		//: the batch is full and a visible message is left: somebody should
		//: look again at once — a consumer loop that is not busy picks it up
		//: immediately.
		if full {
			//: what was leased, due now.
			return walk.out, now, nil
		}
	}
	//: whatever this consumer won, and when the earliest message held back
	//: becomes visible — zero when none is.
	return walk.out, walk.later, nil
}

// leaseWalk is what leaseVisible carries from one queued name to the next.
type leaseWalk struct {
	// out is the batch; nil until something is eligible, so an empty poll
	// allocates nothing.
	out []corequeue.DeliveryValue
	// now is the instant visibility is judged at, in Unix nanoseconds.
	now int64
	// later is the earliest instant a level's walk stopped at.
	later int64
	// max bounds the batch.
	max int
}

// leaseLevel walks one level of the index of ready/ in visibility order: it
// leases each visible message into walk's batch, buries each past its MaxAge,
// and stops at the level's first message not yet visible, whose instant is
// when the next one of the level will be. It reports full when the batch is
// full and a visible message is left.
func (b *fileBroker) leaseLevel(walk *leaseWalk, level int) (full bool, err error) {
	ready := &b.index.ready
	//: until the level is empty, held back, or the batch full.
	for {
		base, found := b.index.head(ready, level)
		//: nothing left at this level.
		if !found {
			//: the next level.
			return false, nil
		}
		name, _ := parseReady(base)
		//: the first one of its level that is not yet visible means none of
		//: the rest of the level are. The next level is walked all the same.
		if name.At > walk.now {
			walk.later = earliest(walk.later, name.At)
			//: the level ends here.
			return false, nil
		}
		expired := messageExpired(b.policy, name.EnqueuedAt, walk.now)
		//: the batch is full and this message is visible.
		if !expired && len(walk.out) == walk.max {
			//: full.
			return true, nil
		}
		//: another goroutine of this broker took it between the two looks.
		if !b.index.take(ready, level, base) {
			continue
		}
		//: what the name decides; the index is read whole again when an
		//: operation fails after taking it, since the name may still be there.
		if stepErr := b.leaseOne(walk, name, base, expired); stepErr != nil {
			b.index.invalidate(ready)
			//: QueueBackendFailed.
			return false, stepErr
		}
	}
}

// leaseOne leases the queued message base names into walk's batch, or buries
// it when it is past its MaxAge.
func (b *fileBroker) leaseOne(walk *leaseWalk, name nameValue, base string, expired bool) error {
	//: a visible message past its MaxAge is never delivered: buried, as a
	//: lease with no attempt left is, and the walk moves on.
	if expired {
		//: QueueBackendFailed when the medium refuses.
		return b.expireReady(name, base, walk.now)
	}
	delivery, leased, leaseErr := b.takeLease(name, base, walk.now)
	//: another consumer won the rename; that is not a failure, it is the
	//: exclusion mechanism working.
	if leased {
		walk.out = append(walk.out, delivery)
	}
	//: QueueBackendFailed when the medium refused for a reason that is not
	//: "somebody beat us".
	return leaseErr
}

// takeLease moves one queued message into flight and reads it back.
//
// The rename IS the exclusion. Two consumers — in one process or in two —
// both attempt it; the kernel gives it to one and gives the other ENOENT.
// There is no lock to hold, nothing for a dead process to keep, and the
// resolution is identical for goroutines and for processes, which ADR 0052
// measured that flock(2) is emphatically not.
func (b *fileBroker) takeLease(
	name nameValue, base string, now int64,
) (delivery corequeue.DeliveryValue, leased bool, err error) {
	lease, entropyErr := randomHex(leaseEntropyBytes)
	//: two consumers leasing two messages in the same nanosecond must not be
	//: able to collide on an in-flight name, because a rename onto an existing
	//: name would DESTROY the other consumer's message.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing moved.
		return corequeue.DeliveryValue{}, false, backendFailed("rand", "", entropyErr)
	}
	leasedName := nameValue{
		At: now + int64(b.policy.VisibilityTimeout), EnqueuedAt: name.EnqueuedAt,
		Entropy: name.Entropy, Deliveries: name.Deliveries + 1, Lease: lease, Priority: name.Priority,
	}
	target := leasedName.inflightName()
	renameErr := b.root.Rename(path.Join(dirReady, base), path.Join(dirInflight, target))
	//: ENOENT is "somebody else took it", which is the ordinary outcome under
	//: contention and is not an error at any layer.
	if renameErr != nil {
		//: a rename that lost the race may still have made its target (see
		//: dropStale) — a lease nobody holds, delivered again once it lapses.
		//: The target carries this call's random lease, so a name there is
		//: ours alone to remove.
		if os.IsNotExist(renameErr) {
			//: QueueBackendFailed when the medium refuses the removal.
			if dropErr := b.dropStale(path.Join(dirInflight, target)); dropErr != nil {
				return corequeue.DeliveryValue{}, false, dropErr
			}
		}
		//: not-exist becomes (false, nil); anything else is the medium.
		return corequeue.DeliveryValue{}, false, ignoreMissing("rename", base, renameErr)
	}
	//: the winner's rename must have taken the queued name with it; one left
	//: behind would be leased again by the next consumer.
	if dropErr := b.dropStale(path.Join(dirReady, base)); dropErr != nil {
		//: QueueBackendFailed.
		return corequeue.DeliveryValue{}, false, dropErr
	}
	//: no fsync on the way in. A crash before the rename reaches the device
	//: leaves the message QUEUED, which is a redelivery — already permitted.
	return b.readLeased(leasedName, target)
}

// readLeased reads the payload of a message this consumer has just leased.
func (b *fileBroker) readLeased(
	name nameValue, receipt string,
) (delivery corequeue.DeliveryValue, leased bool, err error) {
	payload, readErr := b.root.ReadFile(path.Join(dirInflight, receipt))
	//: we hold the lease, so the file is ours; a failure here is the medium.
	if readErr != nil {
		//: QueueBackendFailed.
		return corequeue.DeliveryValue{}, false, backendFailed("read", receipt, readErr)
	}
	//: the delivery, with the count that says whether this is the first time.
	return corequeue.DeliveryValue{
		Message: corequeue.MessageValue{
			ID: name.ID(), Payload: payload, EnqueuedAt: name.EnqueuedTime(),
		},
		Lease: corequeue.LeaseValue{
			Receipt: corequeue.ReceiptValue(receipt), ExpiresAt: name.AtTime(),
		},
		Deliveries: name.Deliveries,
	}, true, nil
}

// reclaim returns every lapsed lease to the queue, or to the dead-letter
// store when it has no attempts left, and returns the deadline, in Unix
// nanoseconds, of the earliest lease still held — zero when none is.
//
// It is what makes the domain's headline promise true: a consumer that is
// SIGKILLed never acknowledges and never nacks, so its message is recovered
// by this scan and by nothing else. A message whose consumers keep dying is
// dead-lettered by the SAME comparison a nack uses — otherwise the poison
// message that killed the process would be redelivered forever, killing every
// consumer that touched it.
func (b *fileBroker) reclaim(now int64) (next int64, err error) {
	inflight := &b.index.inflight
	//: the queued changes applied, or the directory read again.
	if syncErr := b.index.sync(b, dirInflight); syncErr != nil {
		//: QueueBackendFailed.
		return 0, syncErr
	}
	//: in lease-deadline order, for the same reason the ready walk is in
	//: visibility order.
	for {
		base, found := b.index.head(inflight, 0)
		//: no lease held.
		if !found {
			//: every lapsed lease is now queued again or abandoned.
			return 0, nil
		}
		name, _ := parseInflight(base)
		//: the first lease that is still held means every remaining one is,
		//: and it is the next one that can lapse.
		if name.At > now {
			//: nothing else to reclaim.
			return name.At, nil
		}
		//: another goroutine of this broker took it between the two looks.
		if !b.index.take(inflight, 0, base) {
			continue
		}
		//: whichever consumer gets here first wins the rename; the others get
		//: ENOENT and move on.
		if recoverErr := b.recoverOne(name, base, now); recoverErr != nil {
			b.index.invalidate(inflight)
			//: QueueBackendFailed.
			return 0, recoverErr
		}
	}
}

// recoverOne decides what becomes of one lapsed lease.
func (b *fileBroker) recoverOne(name nameValue, base string, now int64) error {
	//: out of attempts, and nobody ever reported why — which is the signature
	//: of a handler that takes the process down with it, since a process that
	//: dies cannot nack.
	if name.Deliveries >= b.policy.MaxDeliveries {
		//: buried with LEASE_EXPIRED as its reason.
		return b.bury(name, base, causeValue{reason: reasonLeaseExpired}, now)
	}
	requeued := nameValue{
		//: a lapsed lease has ALREADY waited a whole visibility timeout, so it
		//: is not charged the retry delay on top: a crashed consumer must not
		//: be punished harder than a failing one.
		At: now, EnqueuedAt: name.EnqueuedAt, Entropy: name.Entropy, Deliveries: name.Deliveries,
		Priority: name.Priority,
	}
	renameErr := b.root.Rename(path.Join(dirInflight, base), path.Join(dirReady, requeued.readyName()))
	//: another consumer reclaimed it first, which is the ordinary race.
	if renameErr != nil {
		//: nil for the lost race, the medium's refusal otherwise.
		return ignoreMissing("rename", base, renameErr)
	}
	//: the lapsed lease's name must be gone, or it is reclaimed again.
	return b.dropStale(path.Join(dirInflight, base))
}

// dropStale removes name when a rename left it behind, and is nothing when
// it is gone — the answer of every kernel whose rename is one step. OpenBSD's
// FFS renames in two (link the new name, unlink the old), and two consumers
// racing on one message there left it under two names, one queued and one
// leased by nobody (#398: a queued command's restart test saw it, then
// delivered the message twice). The queue's one rule — a message has exactly
// one name, and that name is its state — holds only if the stale one is
// removed. Only a name this broker owns at that instant is ever passed: a
// source it has just renamed away, or a target carrying its own fresh lease.
func (b *fileBroker) dropStale(name string) error {
	//: absent is the ordinary answer.
	return ignoreMissing("remove", name, b.root.Remove(name))
}

// ignoreMissing swallows an absent source — the ordinary outcome of losing a
// rename race — and reports anything else as a backend failure.
func ignoreMissing(op, name string, cause error) error {
	//: nothing went wrong, or somebody else moved it first — which is the
	//: exclusion mechanism working rather than a fault. The two are merged
	//: because they have the same answer: this was not ours to take, and
	//: there is nothing for the caller to do about it.
	if cause == nil || os.IsNotExist(cause) {
		//: the caller reads nil as "carry on".
		return nil
	}
	//: the medium refused for a reason of its own.
	return backendFailed(op, name, cause)
}

// expireReady moves one queued message past the policy's MaxAge to the
// dead-letter store, with MESSAGE_EXPIRED and the count it had.
//
// It claims the message first, by the rename that IS the exclusion — into
// flight, under a lease of its own and with its count unchanged —, so two
// consumers never bury it twice; then it buries what it holds, as a lapsed
// lease with no attempt left is buried. A crash between the two leaves an
// ordinary lease, which lapses, is reclaimed, and is expired again.
func (b *fileBroker) expireReady(name nameValue, base string, now int64) error {
	lease, entropyErr := randomHex(leaseEntropyBytes)
	//: the claim needs a lease name nobody else can hold.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing moved.
		return backendFailed("rand", "", entropyErr)
	}
	claimed := nameValue{
		At: now + int64(b.policy.VisibilityTimeout), EnqueuedAt: name.EnqueuedAt,
		Entropy: name.Entropy, Deliveries: name.Deliveries, Lease: lease, Priority: name.Priority,
	}
	target := claimed.inflightName()
	renameErr := b.root.Rename(path.Join(dirReady, base), path.Join(dirInflight, target))
	//: another consumer took it first — to lease or to bury, both its own.
	if renameErr != nil {
		//: a lost rename may have made its target, which is ours alone.
		if os.IsNotExist(renameErr) {
			//: QueueBackendFailed when the medium refuses the removal.
			if dropErr := b.dropStale(path.Join(dirInflight, target)); dropErr != nil {
				return dropErr
			}
		}
		//: nil for the lost race, the medium's refusal otherwise.
		return ignoreMissing("rename", base, renameErr)
	}
	//: the queued name must not survive the rename that claimed it.
	if dropErr := b.dropStale(path.Join(dirReady, base)); dropErr != nil {
		//: QueueBackendFailed.
		return dropErr
	}
	//: dead, with the reason it died for.
	return b.bury(claimed, target, expiredCause(), now)
}
