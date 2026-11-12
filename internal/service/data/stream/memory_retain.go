package stream

import (
	"context"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// reserve waits until size more bytes fit under PolicyValue.MaxBytes and
// returns with the lock held; on a refusal it returns with the lock released.
//
// Room appears only when records are removed — by retention by age, by Trim —
// so it waits for whichever comes first: a removal (room), the oldest record
// reaching its age, the end of BlockTimeout, or ctx.
func (m *memoryStream) reserve(ctx context.Context, size int64) error {
	var deadline time.Time
	//: until room, the deadline or the caller.
	for {
		m.mu.Lock()
		now := m.clk.Now()
		m.expireAll(now)
		//: room, or no ceiling at all.
		if m.policy.MaxBytes == 0 || m.held+size <= m.policy.MaxBytes {
			//: the lock stays held for the write.
			return nil
		}
		//: the first look fixes the deadline.
		if deadline.IsZero() {
			deadline = now.Add(m.policy.BlockTimeout)
		}
		remaining := deadline.Sub(now)
		//: a batch larger than the ceiling never fits, and a spent wait is spent.
		if remaining <= 0 || size > m.policy.MaxBytes {
			full := streamFull(size, m.held, m.policy.MaxBytes)
			m.mu.Unlock()
			//: StreamFull: nothing was written.
			return full
		}
		wait := m.untilExpiry(now, remaining)
		room := m.room.current()
		m.mu.Unlock()
		//: the caller's cause ends the wait.
		if stopped := m.await(ctx, room, wait); stopped != nil {
			//: the context's cause.
			return stopped
		}
	}
}

// untilExpiry returns how long until the oldest retained record of any
// partition expires, capped at limit. The caller holds the lock.
func (m *memoryStream) untilExpiry(now time.Time, limit time.Duration) time.Duration {
	//: no age retention frees nothing on its own.
	if m.policy.RetainFor == 0 {
		//: only a Trim or the deadline.
		return limit
	}
	wait := limit
	//: the oldest record of each partition is its first.
	for i := range m.parts {
		//: an empty partition frees nothing.
		if len(m.parts[i].entries) == 0 {
			continue
		}
		due := m.parts[i].entries[0].AppendedAt.Add(m.policy.RetainFor).Sub(now)
		//: an earlier expiry.
		if due > 0 && due < wait {
			wait = due
		}
	}
	//: the shortest wait that may bring room.
	return wait
}

// await waits for room, for wait on the stream's clock, or for ctx.
func (m *memoryStream) await(ctx context.Context, room <-chan struct{}, wait time.Duration) error {
	timer := m.clk.NewTimer(wait)
	defer timer.Stop()
	//: whichever comes first.
	select {
	case <-ctx.Done():
		//: the caller gave up.
		return context.Cause(ctx)
	case <-room:
		//: something was removed: look again.
		return nil
	case <-timer.C():
		//: a record may have aged out, or the deadline passed: look again.
		return nil
	}
}

// expireAll removes, from every partition, the records past RetainFor at
// now. The caller holds the lock exclusively.
func (m *memoryStream) expireAll(now time.Time) {
	//: every partition ages on the same clock.
	for i := range m.parts {
		m.drop(&m.parts[i], m.expired(&m.parts[i], now))
	}
}

// retainBytes removes part's oldest records while it holds more than
// PolicyValue.RetainBytes, sparing the newest batch of fresh records: a
// batch just acknowledged as appended is never trimmed by its own Append.
func (m *memoryStream) retainBytes(part *memPartition, fresh int) {
	//: zero is unbounded.
	if m.policy.RetainBytes == 0 {
		return
	}
	excess := part.bytes - m.policy.RetainBytes
	count := 0
	//: oldest first, never into the batch just written.
	for count < len(part.entries)-fresh && excess > 0 {
		excess -= recordBytes(part.entries[count].Record)
		count++
	}
	m.drop(part, count)
}

// drop removes part's count oldest records, releasing their bytes, and wakes
// an Append waiting for room. The caller holds the lock exclusively.
func (m *memoryStream) drop(part *memPartition, count int) {
	//: nothing to remove wakes nobody.
	if count == 0 {
		return
	}
	var freed int64
	//: each removed entry is cleared, so its bytes can be collected.
	for i := range count {
		freed += recordBytes(part.entries[i].Record)
		part.entries[i] = corestream.EntryValue{}
	}
	part.entries = part.entries[count:]
	//: an emptied partition lets its array go.
	if len(part.entries) == 0 {
		part.entries = nil
	}
	part.first += corestream.Offset(count)
	part.bytes -= freed
	m.held -= freed
	m.room.fire()
}

// Trim makes before the first offset partition retains.
func (m *memoryStream) Trim(_ context.Context, partition int, before corestream.Offset) error {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	part := &m.parts[partition]
	m.drop(part, m.expired(part, m.clk.Now()))
	//: a record that does not exist yet.
	if before > part.next() {
		//: OffsetAhead, with both offsets.
		return offsetAhead(before, part.next())
	}
	//: at or below the first offset changes nothing.
	if before <= part.first {
		return nil
	}
	m.drop(part, int(before-part.first))
	//: trimmed; the next offset does not move.
	return nil
}
