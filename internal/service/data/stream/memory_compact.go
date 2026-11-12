package stream

import (
	"context"
	"sort"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// Compact removes from partition every compactable record a later one of its
// key supersedes, and every tombstone past its second window, in place.
func (m *memoryStream) Compact(_ context.Context, partition int) (corestream.CompactValue, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.CompactValue{}, refused
	}
	//: zero never compacts.
	if m.policy.CompactAfter == 0 {
		//: nothing looked at.
		return corestream.CompactValue{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clk.Now()
	part := &m.parts[partition]
	m.drop(part, m.expired(part, now))
	lines := compactLinesAt(now, m.policy.CompactAfter)
	old := sort.Search(len(part.entries), func(i int) bool {
		//: younger than CompactAfter: never touched.
		return !lines.compactable(part.entries[i].AppendedAt)
	})
	last := make(map[string]int)
	//: the last compactable record of each key.
	for i := range old {
		noteLast(last, part.entries[i].Record.Key, i)
	}
	var dropped, freed int64
	kept := 0
	//: survivors slide down in place, keeping their offsets.
	for i, entry := range part.entries {
		//: a compactable record that goes.
		if i < old && lines.drops(entry.Record, entry.AppendedAt, lastOf(last, entry.Record.Key) == i) {
			dropped++
			freed += recordBytes(entry.Record)
			continue
		}
		part.entries[kept] = entry
		kept++
	}
	clear(part.entries[kept:])
	part.entries = part.entries[:kept]
	part.bytes -= freed
	m.held -= freed
	//: an Append waiting for room looks again.
	if dropped > 0 {
		m.room.fire()
	}
	//: what was looked at, kept and dropped.
	return compactResult(int64(old), dropped), nil
}
