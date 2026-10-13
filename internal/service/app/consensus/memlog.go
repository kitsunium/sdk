package consensus

import (
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MemoryLog is the in-memory LogStore double (D2): every port contract
// except durability, which it has none of. Sync can be made to fail, so a
// test drives STORAGE_FAILED.
type MemoryLog struct {
	mu        sync.RWMutex
	first     uint64
	prevTerm  uint64
	ents      []coreconsensus.Entry
	hs        coreconsensus.HardState
	stamp     coreconsensus.Stamp
	stamped   bool
	failSync  error
	closed    bool
	syncCount int
}

// NewMemoryLog returns an empty in-memory log.
func NewMemoryLog() *MemoryLog { return &MemoryLog{first: 1} }

func compacted(index, first uint64) error {
	return errs.Wrap(coreconsensus.Compacted, errs.WrapParams{},
		errs.String("index", u64s(index)), errs.String("first", u64s(first)))
}

// Entries implements LogReader.
func (l *MemoryLog) Entries(lo, hi uint64, maxBytes int, dst []coreconsensus.Entry) ([]coreconsensus.Entry, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if lo < l.first {
		return dst, compacted(lo, l.first)
	}
	last := l.first + uint64(len(l.ents)) - 1
	if hi > last+1 {
		return dst, storageFailed("entries", errImpossible)
	}
	size := 0
	for i := lo; i < hi; i++ {
		e := l.ents[i-l.first]
		size += len(e.Data)
		if i > lo && size > maxBytes {
			break
		}
		dst = append(dst, e)
	}
	return dst, nil
}

// Term implements LogReader.
func (l *MemoryLog) Term(index uint64) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	switch {
	case index == l.first-1:
		return l.prevTerm, nil
	case index < l.first:
		return 0, compacted(index, l.first)
	case index >= l.first+uint64(len(l.ents)):
		return 0, storageFailed("term", errImpossible)
	}
	return l.ents[index-l.first].Position.Term, nil
}

// Bounds implements LogReader.
func (l *MemoryLog) Bounds() (first, last uint64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.first, l.first + uint64(len(l.ents)) - 1
}

// Append implements LogStore.
func (l *MemoryLog) Append(entries []coreconsensus.Entry, hs *coreconsensus.HardState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range entries {
		if e.Position.Index != l.first+uint64(len(l.ents)) {
			return storageFailed("append", errImpossible)
		}
		l.ents = append(l.ents, e)
	}
	if hs != nil {
		l.hs = *hs
	}
	return nil
}

// Sync implements LogStore.
func (l *MemoryLog) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.syncCount++
	return l.failSync
}

// FailSync makes every later Sync return err.
func (l *MemoryLog) FailSync(err error) {
	l.mu.Lock()
	l.failSync = err
	l.mu.Unlock()
}

// TruncateSuffix implements LogStore.
func (l *MemoryLog) TruncateSuffix(from uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if from < l.first {
		from = l.first
	}
	if n := from - l.first; n < uint64(len(l.ents)) {
		l.ents = l.ents[:n]
	}
	return nil
}

// CompactPrefix implements LogStore; compacting past the end leaves an
// empty log at (through+1, through).
func (l *MemoryLog) CompactPrefix(through uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if through < l.first {
		return nil
	}
	last := l.first + uint64(len(l.ents)) - 1
	if through >= last {
		if through == last && len(l.ents) > 0 {
			l.prevTerm = l.ents[len(l.ents)-1].Position.Term
		} else {
			l.prevTerm = 0
		}
		l.ents = nil
		l.first = through + 1
		return nil
	}
	l.prevTerm = l.ents[through-l.first].Position.Term
	l.ents = append([]coreconsensus.Entry(nil), l.ents[through-l.first+1:]...)
	l.first = through + 1
	return nil
}

// HardState implements LogStore.
func (l *MemoryLog) HardState() (coreconsensus.HardState, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.hs, nil
}

// Stamp implements LogStore.
func (l *MemoryLog) Stamp() (coreconsensus.Stamp, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stamp, nil
}

// WriteStamp implements LogStore.
func (l *MemoryLog) WriteStamp(s coreconsensus.Stamp) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stamp, l.stamped = s, true
	return nil
}

// Close implements LogStore.
func (l *MemoryLog) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}
