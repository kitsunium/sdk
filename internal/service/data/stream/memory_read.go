package stream

import (
	"context"
	"sort"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// checkPartition refuses a partition outside [0, Partitions()).
func (m *memoryStream) checkPartition(partition int) error {
	//: the one range every partition argument is held to.
	if partition < 0 || partition >= len(m.parts) {
		//: UnknownPartition.
		return unknownPartition(partition, len(m.parts))
	}
	//: a partition of this stream.
	return nil
}

// retained returns the offset of partition's first record still young enough
// to read at now, and the index of that record in its entries. The caller
// holds the lock, shared or exclusive.
func (m *memoryStream) retained(part *memPartition, now time.Time) (corestream.Offset, int) {
	skip := m.expired(part, now)
	//: the records retention has already claimed are skipped, not removed.
	return part.first + corestream.Offset(skip), skip
}

// expired returns how many of partition's oldest entries are past
// PolicyValue.RetainFor at now: a record expires once its age reaches the
// bound. The entries are sorted by AppendedAt (stamp), so it is a search.
func (m *memoryStream) expired(part *memPartition, now time.Time) int {
	//: zero keeps every record for good.
	if m.policy.RetainFor == 0 {
		//: nothing expires.
		return 0
	}
	//: the first record still younger than the bound.
	return sort.Search(len(part.entries), func(i int) bool {
		//: younger than RetainFor: kept.
		return now.Sub(part.entries[i].AppendedAt) < m.policy.RetainFor
	})
}

// Read returns up to maxEntries entries of partition from offset from on.
func (m *memoryStream) Read(
	_ context.Context, partition int, from corestream.Offset, maxEntries int,
) ([]corestream.EntryValue, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return nil, refused
	}
	//: a loop asking for nothing would process nothing forever.
	if maxEntries <= 0 {
		//: InvalidBatchSize.
		return nil, invalidBatchSize("max", maxEntries)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	part := &m.parts[partition]
	first, skip := m.retained(part, m.clk.Now())
	//: the records are gone, and starting later would hide the gap.
	if from < first {
		//: OffsetTrimmed, with both offsets.
		return nil, offsetTrimmed(from, first)
	}
	//: a record that does not exist yet.
	if from > part.next() {
		//: OffsetAhead, with both offsets.
		return nil, offsetAhead(from, part.next())
	}
	start := skip + int(from-first)
	end := min(start+maxEntries, len(part.entries))
	//: the reader's own copy; empty at the next offset.
	return copyEntries(part.entries[start:end]), nil
}

// Bounds returns partition's first retained offset and its next one.
func (m *memoryStream) Bounds(_ context.Context, partition int) (corestream.BoundsValue, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.BoundsValue{}, refused
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	part := &m.parts[partition]
	first, _ := m.retained(part, m.clk.Now())
	//: the retained range.
	return corestream.BoundsValue{First: first, Next: part.next()}, nil
}

// OffsetAt returns partition's first offset appended at or after t.
func (m *memoryStream) OffsetAt(
	_ context.Context, partition int, t time.Time,
) (corestream.Offset, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	part := &m.parts[partition]
	first, skip := m.retained(part, m.clk.Now())
	kept := part.entries[skip:]
	//: AppendedAt never decreases, so the first one at or after t is a search.
	found := sort.Search(len(kept), func(i int) bool {
		//: at or after t.
		return !kept[i].AppendedAt.Before(t)
	})
	//: the first retained offset when t precedes them all, next when it follows.
	return first + corestream.Offset(found), nil
}

// Wake returns the channel the next Append closes.
func (m *memoryStream) Wake() <-chan struct{} {
	//: armed before the caller's Read, so an append in between is not missed.
	return m.wake.current()
}
