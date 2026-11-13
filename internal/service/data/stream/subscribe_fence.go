package stream

import (
	"context"
	"strconv"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
)

// renewShare is the part of a lease's life after which it is renewed: a
// third, the queue consumer's AutoExtend cadence, so two renewals may fail
// before the lease ends.
const renewShare int64 = 3

// heldFence is the lease a worker reads its partition under.
type heldFence struct {
	lease corelock.Lease
	// renewAt is when the lease is renewed next; zero for a lease that
	// cannot expire, which is never renewed.
	renewAt time.Time
	// every is the time between two renewals.
	every time.Duration
}

// fenceName is the lock a worker holds on partition.
func (w *subscriber) fenceName(partition int) string {
	//: the stream, the group and the partition, so no two readers of one
	//: partition for one group share a name with anything else.
	return "stream:" + w.cfg.Name + ":" + w.cfg.Group + ":" + strconv.Itoa(partition)
}

// acquire takes the partition's fence once: held false when another process
// holds it. Without a Fence every partition is held, by a nil fence.
func (w *subscriber) acquire(ctx context.Context, partition int) (*heldFence, bool, error) {
	//: one process runs the group: nothing to hold.
	if w.cfg.Fence == nil {
		return nil, true, nil
	}
	lease, held, err := w.cfg.Fence.TryAcquire(ctx, w.fenceName(partition))
	//: refused, or held elsewhere.
	if err != nil || !held {
		//: the locker's error, or a standby.
		return nil, false, err
	}
	fence := &heldFence{lease: lease}
	//: a lease that can expire is renewed a third of its life on.
	if deadliner, ok := lease.(corelock.Deadliner); ok {
		now := w.cfg.Clock.Now()
		fence.every = max(deadliner.Deadline().Sub(now)/time.Duration(renewShare), 0)
		fence.renewAt = now.Add(fence.every)
	}
	//: held.
	return fence, true, nil
}

// renew renews fence when its renewal is due and reports whether it is still
// held; no fence is always held.
func (w *subscriber) renew(ctx context.Context, fence *heldFence) bool {
	//: nothing to renew, or a lease that cannot be taken away.
	if fence == nil || fence.renewAt.IsZero() {
		return true
	}
	now := w.cfg.Clock.Now()
	//: not due yet.
	if now.Before(fence.renewAt) {
		return true
	}
	//: refused — LOCK_NOT_HELD, or a locker that cannot answer: either way
	//: this worker no longer knows it is the partition's only reader.
	if fence.lease.Extend(ctx) != nil {
		return false
	}
	fence.renewAt = w.cfg.Clock.Now().Add(fence.every)
	//: renewed.
	return true
}

// bounded shortens a wait of d to the fence's next renewal, so a long wait
// never outlasts the lease it is held under.
func (w *subscriber) bounded(d time.Duration, fence *heldFence) time.Duration {
	//: nothing to renew.
	if fence == nil || fence.renewAt.IsZero() {
		return d
	}
	//: the earlier of the two.
	return min(d, fence.renewAt.Sub(w.cfg.Clock.Now()))
}

// release gives the fence up, bounded past a stop; a lease already lost
// releases nothing, which is not a failure.
func (w *subscriber) release(ctx context.Context, fence *heldFence) {
	//: nothing held.
	if fence == nil {
		return
	}
	//: the answer is not needed: a lease not released lapses, or dies
	//: with this process.
	abandon(w.settled(ctx, fence.lease.Release))
}
