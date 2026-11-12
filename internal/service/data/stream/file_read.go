package stream

import (
	"context"
	"math"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/seglog"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// checkPartition refuses a partition outside [0, Partitions()).
func (m *fileStream) checkPartition(partition int) error {
	//: the one range every partition argument is held to.
	if partition < 0 || partition >= len(m.parts) {
		//: UnknownPartition.
		return unknownPartition(partition, len(m.parts))
	}
	//: a partition of this stream.
	return nil
}

// Read returns up to maxEntries entries of partition from offset from on,
// read from the segments with one read per contiguous run.
func (m *fileStream) Read(
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
	//: a closed stream reads nothing.
	if closed := m.usable(); closed != nil {
		//: the closed refusal.
		return nil, closed
	}
	part := m.parts[partition]
	part.mu.RLock()
	first, next := part.first(m.clk.Now(), m.policy.RetainFor), part.next()
	part.mu.RUnlock()
	//: the records are gone, and starting later would hide the gap.
	if from < first {
		//: OffsetTrimmed, with both offsets.
		return nil, offsetTrimmed(from, first)
	}
	//: a record that does not exist yet.
	if from > next {
		//: OffsetAhead, with both offsets.
		return nil, offsetAhead(from, next)
	}
	hi := from + corestream.Offset(min(uint64(maxEntries), uint64(next-from)))
	//: empty at the next offset.
	if hi == from {
		//: nothing, and no error.
		return nil, nil
	}
	payloads, err := part.log.Range(uint64(from), uint64(hi), math.MaxInt, make([][]byte, 0, hi-from))
	//: a Trim landing between the bounds and the read.
	if errs.HasCode(err, seglog.CodeDropped) {
		//: OffsetTrimmed, as a read after the Trim answers.
		return nil, offsetTrimmed(from, m.firstOf(part))
	}
	//: the device refused, or a record no longer matches what was written.
	if err != nil {
		//: the log's refusal, in this engine's trail.
		return nil, backendFailed("read", "", err)
	}
	//: the payloads are the reader's own, so the entries alias them.
	return decodeEntries(partition, from, payloads)
}

// firstOf returns part's first readable offset now.
func (m *fileStream) firstOf(part *filePart) corestream.Offset {
	part.mu.RLock()
	defer part.mu.RUnlock()
	//: the age bound on top of what was claimed.
	return part.first(m.clk.Now(), m.policy.RetainFor)
}

// decodeEntries turns the payloads read from offset from on into entries.
func decodeEntries(partition int, from corestream.Offset, payloads [][]byte) ([]corestream.EntryValue, error) {
	entries := make([]corestream.EntryValue, len(payloads))
	//: one entry per payload, in offset order.
	for i, payload := range payloads {
		stamp, record, ok := decodeRecord(payload)
		//: bytes this engine cannot have written, under a valid checksum.
		if !ok {
			//: StreamBackendFailed: refused rather than skipped.
			return nil, backendFailed("decode", "", nil)
		}
		entries[i] = corestream.EntryValue{
			AppendedAt: timeOf(stamp),
			Record:     record,
			Partition:  partition,
			Offset:     from + corestream.Offset(i),
		}
	}
	//: the reader's entries.
	return entries, nil
}

// Bounds returns partition's first retained offset and its next one.
func (m *fileStream) Bounds(_ context.Context, partition int) (corestream.BoundsValue, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.BoundsValue{}, refused
	}
	part := m.parts[partition]
	part.mu.RLock()
	defer part.mu.RUnlock()
	//: the retained range, from the index alone.
	return corestream.BoundsValue{First: part.first(m.clk.Now(), m.policy.RetainFor), Next: part.next()}, nil
}

// OffsetAt returns partition's first offset appended at or after t: a
// search of the batches' instants, which the index keeps.
func (m *fileStream) OffsetAt(
	_ context.Context, partition int, t time.Time,
) (corestream.Offset, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	part := m.parts[partition]
	part.mu.RLock()
	defer part.mu.RUnlock()
	//: the first retained offset when t precedes them all, next when it follows.
	return part.offsetAt(part.first(m.clk.Now(), m.policy.RetainFor), t), nil
}

// Wake returns the channel the next Append closes.
func (m *fileStream) Wake() <-chan struct{} {
	//: armed before the caller's Read, so an append in between is not missed.
	return m.wake.current()
}
