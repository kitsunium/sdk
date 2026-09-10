// Package queue — the durable broker's read path: reclaiming the leases of
// consumers that died, and leasing what is visible.
package queue

import (
	"context"
	"os"
	"path"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
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
	//: BEFORE anything is handed out.
	if reclaimErr := b.reclaim(now); reclaimErr != nil {
		//: QueueBackendFailed — the medium refused mid-recovery.
		return nil, reclaimErr
	}
	//: whatever is visible, oldest first.
	return b.leaseVisible(now, max)
}

// leaseVisible walks the queued messages in visibility order and leases up to
// max of them.
func (b *fileBroker) leaseVisible(
	now int64, max int,
) (batch []corequeue.DeliveryValue, err error) {
	entries, listErr := b.entriesOf(dirReady)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return nil, listErr
	}
	//: nil until something is eligible, so an empty poll allocates nothing.
	var out []corequeue.DeliveryValue
	//: in visibility order, because fs.ReadDir sorts and the name opens with
	//: a zero-padded instant.
	for _, entry := range entries {
		name, ok := parseReady(entry.Name())
		//: anything that is not one of our messages — notably the `.vfs-*.tmp`
		//: temporary a concurrent atomic publication is still writing, which
		//: MUST NOT be delivered as a half-written message.
		if !ok {
			continue
		}
		//: the first one that is not yet visible means none of the rest are.
		if name.At > now {
			break
		}
		delivery, leased, leaseErr := b.takeLease(name, entry.Name(), now)
		//: the medium refused for a reason that is not "somebody beat us".
		if leaseErr != nil {
			//: QueueBackendFailed.
			return nil, leaseErr
		}
		//: another consumer won the rename; that is not a failure, it is the
		//: exclusion mechanism working.
		if !leased {
			continue
		}
		out = append(out, delivery)
		//: the caller asked for a bounded batch.
		if len(out) == max {
			break
		}
	}
	//: whatever this consumer won.
	return out, nil
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
		Entropy: name.Entropy, Deliveries: name.Deliveries + 1, Lease: lease,
	}
	target := leasedName.inflightName()
	renameErr := b.root.Rename(path.Join(dirReady, base), path.Join(dirInflight, target))
	//: ENOENT is "somebody else took it", which is the ordinary outcome under
	//: contention and is not an error at any layer.
	if renameErr != nil {
		//: not-exist becomes (false, nil); anything else is the medium.
		return corequeue.DeliveryValue{}, false, ignoreMissing("rename", base, renameErr)
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
// store when it has no attempts left.
//
// It is what makes the domain's headline promise true: a consumer that is
// SIGKILLed never acknowledges and never nacks, so its message is recovered
// by this scan and by nothing else. A message whose consumers keep dying is
// dead-lettered by the SAME comparison a nack uses — otherwise the poison
// message that killed the process would be redelivered forever, killing every
// consumer that touched it.
func (b *fileBroker) reclaim(now int64) error {
	entries, listErr := b.entriesOf(dirInflight)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return listErr
	}
	//: in lease-deadline order, for the same reason the ready scan is in
	//: visibility order.
	for _, entry := range entries {
		name, ok := parseInflight(entry.Name())
		//: not one of ours.
		if !ok {
			continue
		}
		//: the first lease that is still held means every remaining one is.
		if name.At > now {
			break
		}
		//: whichever consumer gets here first wins the rename; the others get
		//: ENOENT and move on.
		if recoverErr := b.recoverOne(name, entry.Name(), now); recoverErr != nil {
			//: QueueBackendFailed.
			return recoverErr
		}
	}
	//: every lapsed lease is now queued again or abandoned.
	return nil
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
	}
	renameErr := b.root.Rename(path.Join(dirInflight, base), path.Join(dirReady, requeued.readyName()))
	//: another consumer reclaimed it first, which is the ordinary race.
	return ignoreMissing("rename", base, renameErr)
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
