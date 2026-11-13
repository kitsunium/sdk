package stream

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// memPartition is one partition of a memory stream: its retained entries, in
// offset order, the offset of the first of them and the next one.
type memPartition struct {
	// entries holds the retained records in offset order. A compaction
	// leaves holes, so an entry's offset is its own and never its index.
	entries []corestream.EntryValue
	// first is the lowest offset still readable. Trimming raises it; nothing
	// lowers it, and a compaction leaves it where it is.
	first corestream.Offset
	// nextOffset is the offset the next record takes; it never moves back.
	nextOffset corestream.Offset
	// bytes is the key and payload bytes the retained entries hold.
	bytes int64
}

// next returns the offset the partition's next record will take.
func (p *memPartition) next() corestream.Offset {
	//: kept apart from the entries, which may end in a hole.
	return p.nextOffset
}

// index returns the position in entries of the first entry at or past
// offset.
func (p *memPartition) index(offset corestream.Offset) int {
	//: the entries are sorted by offset.
	return sort.Search(len(p.entries), func(i int) bool {
		//: at or past offset.
		return p.entries[i].Offset >= offset
	})
}

// cursorKey names one group's cursor on one partition.
type cursorKey struct {
	group     string
	partition int
}

// memoryStream is a stream that lives entirely in one process's heap.
//
// # What it is for, and what it is not
//
// It is the test double. A consumer's suite gets the port's semantics —
// contiguous offsets, per-key placement, retention, every refusal — with no
// directory and no flush, and the conformance suite holds it to the same
// cases as every durable engine, so it cannot drift from them. It is NOT a
// durable stream: a restart is a total loss. Use the file or the SQL engine.
//
// Retention by age is LAZY, as on the file engine: nothing here runs a timer.
// A read computes the first record still young enough and skips the rest; an
// Append, a Trim or a backpressure wait removes them for good.
type memoryStream struct {
	// last is the latest instant a batch was stamped with: AppendedAt never
	// goes backwards, so a partition's entries stay sorted by it and
	// OffsetAt and the age retention can binary-search them.
	last time.Time
	clk  clock.Timed
	// wake is closed by every Append (corestream.Waker).
	wake *wakeSignal
	// room is closed whenever records are removed, which is what an Append
	// waiting under PolicyValue.MaxBytes waits for.
	room *wakeSignal
	// cursors holds every group's committed offset (corestream.Committer).
	cursors map[cursorKey]corestream.Offset
	parts   []memPartition
	policy  corestream.PolicyValue
	// held is the bytes every partition retains, the figure MaxBytes bounds.
	held int64
	// turn is the partition the next unkeyed batch goes to.
	turn int
	// mu is an RWMutex because Read, Bounds, OffsetAt and Committed are
	// genuine reads: retention by age is computed, not applied, under it.
	mu sync.RWMutex
}

// NewMemory returns a stream whose partitions live in the heap of this
// process: the test double of every durable engine.
//
// It refuses a policy it cannot honour at CONSTRUCTION, through the same
// core/data/stream.PolicyValue.Validate every engine runs, which is what
// makes it an honest double. The stream it returns also answers
// [corestream.Waker], [corestream.Seeker], [corestream.Trimmer],
// [corestream.Compactor] and [corestream.Committer]; it keeps nothing past
// the process, so a restart
// is a total loss.
func NewMemory(cfg MemoryConfig) (s corestream.Stream, err error) {
	//: the shared guard, so every engine refuses identical inputs identically.
	if invalid := cfg.Policy.Validate(); invalid != nil {
		//: StreamMisconfigured, naming the field.
		return nil, invalid
	}
	policy := cfg.Policy.Normalized()
	//: usable.
	return &memoryStream{
		clk:     clockOrSystem(cfg.Clock),
		wake:    newWakeSignal(),
		room:    newWakeSignal(),
		cursors: map[cursorKey]corestream.Offset{},
		parts:   make([]memPartition, policy.Partitions),
		policy:  policy,
	}, nil
}

// Partitions returns how many partitions the stream has.
func (m *memoryStream) Partitions() int {
	//: fixed at construction, so no lock.
	return len(m.parts)
}

// Append writes records to one partition as one batch.
func (m *memoryStream) Append(
	ctx context.Context, records []corestream.RecordValue,
) (corestream.AppendValue, error) {
	//: a caller that has already given up writes nothing.
	if ctx.Err() != nil {
		//: the caller's own cause.
		return corestream.AppendValue{}, context.Cause(ctx)
	}
	size, refused := admit(records, m.policy.MaxRecordBytes)
	//: the whole batch is refused before anything is written.
	if refused != nil {
		//: InvalidBatchSize or RecordTooLarge.
		return corestream.AppendValue{}, refused
	}
	partition, keyed, mixed := keyedPartition(records, len(m.parts))
	//: a batch is one partition's.
	if mixed != nil {
		//: PartitionsMixed.
		return corestream.AppendValue{}, mixed
	}
	//: reserve returns with the lock held, and only on success.
	if full := m.reserve(ctx, size); full != nil {
		//: StreamFull, or the context's cause.
		return corestream.AppendValue{}, full
	}
	defer m.mu.Unlock()
	//: an unkeyed batch takes its turn.
	if !keyed {
		partition = m.turn
		m.turn = (m.turn + 1) % len(m.parts)
	}
	appended := m.store(partition, records, size)
	m.wake.fire()
	//: where the batch landed.
	return appended, nil
}

// store copies records into partition as one batch stamped with one instant,
// then applies RetainBytes to that partition. The caller holds the lock.
func (m *memoryStream) store(
	partition int, records []corestream.RecordValue, size int64,
) corestream.AppendValue {
	part := &m.parts[partition]
	first := part.next()
	stamp := m.stamp()
	arena := make([]byte, 0, size)
	//: one copy of every byte, so the caller may reuse its buffers at once.
	for i, record := range records {
		var key, payload []byte
		arena, key = carve(arena, record.Key)
		arena, payload = carve(arena, record.Payload)
		part.entries = append(part.entries, corestream.EntryValue{
			AppendedAt: stamp,
			Record:     corestream.RecordValue{Key: key, Payload: payload},
			Partition:  partition,
			Offset:     first + corestream.Offset(i),
		})
	}
	part.nextOffset += corestream.Offset(len(records))
	part.bytes += size
	m.held += size
	m.retainBytes(part, len(records))
	//: the batch's range.
	return corestream.AppendValue{
		Partition: partition,
		First:     first,
		Last:      first + corestream.Offset(len(records)-1),
	}
}

// stamp returns the instant the batch being appended is stamped with: the
// clock's, or the last stamp when the clock went backwards.
func (m *memoryStream) stamp() time.Time {
	now := m.clk.Now()
	//: never before the previous batch, so the entries stay sorted by time.
	if now.Before(m.last) {
		now = m.last
	}
	m.last = now
	//: the batch's one instant.
	return now
}
