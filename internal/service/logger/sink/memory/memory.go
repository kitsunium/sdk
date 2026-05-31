// Package memory — defines the Memory sink, an in-memory Sink that buffers a
// defensive snapshot of every received RecordEvent for test assertions.
package memory

import (
	"context"
	"slices"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// Memory is a terminal Sink that retains a defensive snapshot of every
// RecordEvent it receives in a slice guarded by an RWMutex. It exists for tests
// that assert on what was logged without parsing an encoder's byte output.
// Write records; Records returns an independent copy of the buffer; Reset
// clears it. All methods are safe for concurrent use.
type Memory struct {
	// mu guards records and writes; Write/Reset take the write lock, Records
	// the read lock so concurrent assertions never block each other.
	mu sync.RWMutex
	// records holds a defensive copy of each received RecordEvent in arrival
	// order; each entry's Attrs slice is cloned so caller mutation cannot
	// corrupt recorded history.
	records []corelogger.RecordEvent
	// writes counts accepted Write calls; it lets callers assert on total
	// throughput (including dropped duplicates) without diffing the slice and
	// gives the struct a second field beyond the guarded slice.
	writes int
}

// NewMemory returns an empty Memory sink ready to record received records.
func NewMemory() *Memory {
	//: the zero value is usable; hand back a pointer so methods share state.
	return &Memory{}
}

// Write appends a defensive snapshot of r to the buffer and reports len(p) as
// written. The encoder's bytes p are intentionally ignored — the sink retains
// the structured record, not its serialised form. The Attrs slice is cloned so
// later caller mutation cannot corrupt recorded history.
func (m *Memory) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the append entirely.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation cause so errors.Is(ctx.Err()) still matches.
		return 0, ctx.Err()
	}
	//: clone the Attrs slice so the snapshot detaches from the caller's slice.
	snapshot := r
	snapshot.Attrs = slices.Clone(r.Attrs)
	//: serialise the append against concurrent writers and readers.
	m.mu.Lock()
	m.records = append(m.records, snapshot)
	m.writes++
	m.mu.Unlock()
	//: report the encoder payload length so the Handler's byte accounting holds.
	return len(p), nil
}

// Flush is a no-op for the memory sink — nothing is buffered downstream.
func (m *Memory) Flush(ctx context.Context) error {
	//: honour cancellation for symmetry with the other sinks.
	if ctx != nil && ctx.Err() != nil {
		//: caller already gave up; surface the cancellation cause.
		return ctx.Err()
	}
	//: nothing is buffered downstream — there is nothing to flush.
	return nil
}

// Close is a no-op for the memory sink — it holds no OS resources. Buffered
// records remain readable via Records after Close so tests can assert on a
// closed sink.
func (m *Memory) Close() error {
	//: the sink owns no descriptors or connections — nothing to release.
	return nil
}

// Records returns a snapshot copy of the buffered records. The returned slice
// is independent of the sink's internal buffer, so callers may iterate it while
// other goroutines continue writing.
func (m *Memory) Records() []corelogger.RecordEvent {
	//: a read lock lets concurrent readers proceed while blocking writers.
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: slices.Clone preserves nil-for-nil so the zero state stays faithful.
	return slices.Clone(m.records)
}

// Len reports the number of accepted Write calls. It is cheaper than len of
// Records for callers that only need a count, and reads under the read lock.
func (m *Memory) Len() int {
	//: a read lock lets concurrent readers proceed while blocking writers.
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: writes tracks every accepted append over the sink's lifetime.
	return m.writes
}

// Reset discards all buffered records and the write counter, returning the
// sink to its empty state.
func (m *Memory) Reset() {
	//: serialise the mutation against concurrent writers and readers.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = nil
	m.writes = 0
}
