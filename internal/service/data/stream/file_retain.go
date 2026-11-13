package stream

import (
	"context"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// reserve waits until size more bytes fit under PolicyValue.MaxBytes, then
// counts them as held, and returns the partition the batch goes to: its
// keys', or the next in turn for an unkeyed one. On a refusal nothing is
// reserved.
//
// Room appears only when records are released — by the age bound, by Trim,
// by the byte bound of another Append — so it waits for whichever comes
// first: a release (room), the oldest record reaching its age, the end of
// BlockTimeout, or ctx. It is the memory engine's wait, on this engine's
// index.
func (m *fileStream) reserve(ctx context.Context, size int64, partition int, keyed bool) (int, error) {
	var deadline time.Time
	//: until room, the deadline or the caller.
	for {
		m.mu.Lock()
		turn := m.tryReserve(&deadline, size, partition, keyed)
		m.mu.Unlock()
		//: a closed stream writes nothing.
		if turn.closed {
			//: the closed refusal.
			return 0, m.usable()
		}
		//: reserved, or refused for good.
		if turn.done {
			return turn.partition, turn.err
		}
		//: the caller's cause ends the wait.
		if stopped := m.await(ctx, turn.room, turn.wait); stopped != nil {
			//: the context's cause.
			return 0, stopped
		}
	}
}

// reservation is what one look at the ceiling decided: the stream is closed,
// the bytes are reserved on partition or refused with err (done), or the
// caller waits wait for room to appear.
type reservation struct {
	err       error
	room      <-chan struct{}
	wait      time.Duration
	partition int
	closed    bool
	done      bool
}

// tryReserve looks once at whether size more bytes fit, counting them as
// held when they do, and fixes *deadline on the first look. The caller
// holds m.mu.
func (m *fileStream) tryReserve(deadline *time.Time, size int64, partition int, keyed bool) reservation {
	//: a closed stream writes nothing; the caller words the refusal, outside
	//: the lock.
	if m.closed {
		return reservation{closed: true}
	}
	now := m.clk.Now()
	//: no ceiling, or room under it once the aged records are released.
	if m.fits(now, size) {
		m.held += size
		//: an unkeyed batch takes its turn.
		if !keyed {
			partition = m.turn
			m.turn = (m.turn + 1) % len(m.parts)
		}
		//: reserved.
		return reservation{done: true, partition: partition}
	}
	//: the first look fixes the deadline.
	if deadline.IsZero() {
		*deadline = now.Add(m.policy.BlockTimeout)
	}
	remaining := deadline.Sub(now)
	//: a batch larger than the ceiling never fits, and a spent wait is spent.
	if remaining <= 0 || size > m.policy.MaxBytes {
		//: StreamFull: nothing was written.
		return reservation{done: true, err: streamFull(size, m.held, m.policy.MaxBytes)}
	}
	return reservation{wait: m.untilExpiry(now, remaining), room: m.room.current()}
}

// fits reports whether size more bytes fit under the ceiling at now, the
// records past their age released first. The caller holds m.mu.
func (m *fileStream) fits(now time.Time, size int64) bool {
	//: no ceiling, or room under it.
	return m.policy.MaxBytes == 0 || m.expireAll(now)+size <= m.policy.MaxBytes
}

// expireAll claims, in every partition, the records past RetainFor at now,
// and returns the bytes still held. The caller holds m.mu; the storage is
// released by the partition's next Append or Trim.
func (m *fileStream) expireAll(now time.Time) int64 {
	//: every partition ages on the same clock.
	for _, part := range m.parts {
		part.mu.Lock()
		m.held -= part.claim(part.first(now, m.policy.RetainFor))
		part.mu.Unlock()
	}
	//: what is left.
	return m.held
}

// untilExpiry returns how long until the oldest retained record of any
// partition expires, capped at limit. The caller holds m.mu.
func (m *fileStream) untilExpiry(now time.Time, limit time.Duration) time.Duration {
	//: no age retention frees nothing on its own.
	if m.policy.RetainFor == 0 {
		//: only a Trim or the deadline.
		return limit
	}
	wait := limit
	//: the oldest retained batch of each partition.
	for _, part := range m.parts {
		part.mu.RLock()
		stamp, found := part.oldestStamp(part.kept)
		part.mu.RUnlock()
		//: an empty partition frees nothing.
		if !found {
			continue
		}
		due := timeOf(stamp).Add(m.policy.RetainFor).Sub(now)
		//: an earlier expiry.
		if due > 0 && due < wait {
			wait = due
		}
	}
	//: the shortest wait that may bring room.
	return wait
}

// await waits for room, for wait on the stream's clock, or for ctx.
func (m *fileStream) await(ctx context.Context, room <-chan struct{}, wait time.Duration) error {
	timer := m.clk.NewTimer(wait)
	defer timer.Stop()
	//: whichever comes first.
	select {
	case <-ctx.Done():
		//: the caller gave up.
		return context.Cause(ctx)
	case <-room:
		//: something was released: look again.
		return nil
	case <-timer.C():
		//: a record may have aged out, or the deadline passed: look again.
		return nil
	}
}

// Trim makes before the first offset partition retains: at once in the
// index, durably by a drop record, and on the device by unlinking every
// sealed segment that then holds nothing readable.
func (m *fileStream) Trim(_ context.Context, partition int, before corestream.Offset) error {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	//: a closed stream trims nothing.
	if closed := m.usable(); closed != nil {
		//: the closed refusal.
		return closed
	}
	part := m.parts[partition]
	part.wmu.Lock()
	freed, err := m.trim(part, before)
	part.wmu.Unlock()
	m.release(freed, false, 0)
	//: trimmed, refused, or the storage's failure.
	return err
}

// trim is Trim on part, whose wmu the caller holds.
func (m *fileStream) trim(part *filePart, before corestream.Offset) (freed int64, err error) {
	//: a partition a failure stopped is not written to again.
	if part.broken != nil {
		//: the failure that stopped it.
		return 0, part.broken
	}
	part.mu.RLock()
	first, next := part.first(m.clk.Now(), m.policy.RetainFor), part.next()
	part.mu.RUnlock()
	//: a record that does not exist yet.
	if before > next {
		//: OffsetAhead, with both offsets.
		return 0, offsetAhead(before, next)
	}
	//: at or below the first offset changes nothing.
	if before <= first {
		//: nothing to do.
		return 0, nil
	}
	//: the drop record, synced, then the segments it empties.
	if dropErr := part.log.DropBefore(uint64(before)); dropErr != nil {
		part.broken = backendFailed("trim", "", dropErr)
		//: StreamBackendFailed.
		return 0, part.broken
	}
	part.mu.Lock()
	freed = part.claim(before)
	part.mu.Unlock()
	//: trimmed; the next offset does not move.
	return freed, nil
}
