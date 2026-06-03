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
	// order; each entry's Attrs slice is deep-cloned (including nested
	// KindGroup payloads) so caller mutation cannot corrupt recorded history.
	records []corelogger.RecordEvent
	// writes is the lifetime accepted-append count — equal to len(records)
	// until Reset (this sink never drops or dedups). It lets callers assert on
	// total throughput without diffing the slice and gives the struct a second
	// field beyond the guarded slice.
	writes int
}

// NewMemory returns an empty Memory sink ready to record received records.
func NewMemory() *Memory {
	//: the zero value is usable; hand back a pointer so methods share state.
	return &Memory{}
}

// Write appends a defensive snapshot of r to the buffer and reports len(p) as
// written. The encoder's bytes p are intentionally ignored — the sink retains
// the structured record, not its serialised form. The Attrs slice is
// deep-cloned, including any nested KindGroup payloads, so later caller
// mutation cannot corrupt recorded history.
func (m *Memory) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the append entirely.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation cause so errors.Is(ctx.Err()) still matches.
		return 0, ctx.Err()
	}
	//: deep-clone the Attrs so the snapshot detaches from the caller's slice,
	//: including any nested KindGroup payloads (V37).
	snapshot := r
	snapshot.Attrs = deepCloneAttrs(r.Attrs)
	//: serialise the append against concurrent writers and readers.
	m.mu.Lock()
	m.records = append(m.records, snapshot)
	m.writes++
	m.mu.Unlock()
	//: report the encoder payload length so the Handler's byte accounting holds.
	return len(p), nil
}

// deepCloneAttrs returns an independent copy of attrs in which every nested
// KindGroup payload is recursively cloned, so later caller mutation of any
// slice handed to GroupValue cannot corrupt recorded history (V37). It
// preserves nil-for-nil so an empty record stays faithful.
func deepCloneAttrs(attrs []corelogger.AttrValue) []corelogger.AttrValue {
	//: nil stays nil so the snapshot matches the caller's zero state.
	if attrs == nil {
		//: nothing to clone — return the nil slice verbatim.
		return nil
	}
	//: copy the outer header+elements; group elements are rebuilt below.
	out := slices.Clone(attrs)
	//: rebuild every grouped element from a recursively cloned nested slice.
	for i := range out {
		//: only KindGroup carries a nested []AttrValue that aliases the caller.
		if out[i].Value.Kind() == corelogger.KindGroup {
			//: reconstruct the group from a deep copy of its children.
			out[i].Value = corelogger.GroupValue(deepCloneAttrs(out[i].Value.Group())...)
		}
	}
	//: hand back the fully detached attribute list.
	return out
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
