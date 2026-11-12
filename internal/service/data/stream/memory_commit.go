package stream

import (
	"context"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// Commit records next as the offset group reads next on partition.
func (m *memoryStream) Commit(
	_ context.Context, group string, partition int, next corestream.Offset,
) error {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return misconfigured("group")
	}
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a cursor past every record is one from another stream.
	if head := m.parts[partition].next(); next > head {
		//: OffsetAhead, with both offsets.
		return offsetAhead(next, head)
	}
	m.cursors[cursorKey{group: group, partition: partition}] = next
	//: kept, below the first retained offset included: the next Read says so.
	return nil
}

// Committed returns the offset group committed last on partition.
func (m *memoryStream) Committed(
	_ context.Context, group string, partition int,
) (corestream.Offset, error) {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return 0, misconfigured("group")
	}
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	next, found := m.cursors[cursorKey{group: group, partition: partition}]
	//: never a zero: where a new group starts is the reader's decision.
	if !found {
		//: UnknownGroup.
		return 0, unknownGroup(group, partition)
	}
	//: the group's cursor.
	return next, nil
}
