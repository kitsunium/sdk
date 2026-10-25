package raft

import (
	"slices"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// memLog is the step function's view of the log: the snapshot base and every
// entry after it. The driver loads it at start and trims it after each
// compaction, so it holds no more than the store retains (D6).
type memLog struct {
	snapIndex uint64
	snapTerm  uint64
	snapChain uint32
	ents      []core.EntryValue
	committed uint64
	stable    uint64
	applied   uint64
	// evicted is the highest index whose payload was dropped from memory;
	// its metadata stays, and so does the payload of an entry below it that
	// the start loaded and is not a command (StartState.Evicted). window
	// counts the payload bytes still resident above it (D6,
	// RecentEntriesBytes).
	evicted uint64
	window  int64
}

// lastIndex is the index of the last entry held, the snapshot base when
// none is.
func (l *memLog) lastIndex() uint64 { return l.snapIndex + uint64(len(l.ents)) }

// lastTerm is the term of the last entry held, the snapshot's term when
// none is: what the election restriction compares.
func (l *memLog) lastTerm() uint64 {
	if len(l.ents) == 0 {
		return l.snapTerm
	}
	return l.ents[len(l.ents)-1].Position.Term
}

// term returns the term at i; ok is false below the snapshot or past the end.
func (l *memLog) term(i uint64) (uint64, bool) {
	switch {
	case i == l.snapIndex:
		return l.snapTerm, true
	case i < l.snapIndex || i > l.lastIndex():
		return 0, false
	}
	return l.ents[i-l.snapIndex-1].Position.Term, true
}

// chain returns the chain value at i, the snapshot's tip at its index.
func (l *memLog) chain(i uint64) uint32 {
	if i == l.snapIndex || i < l.snapIndex || i > l.lastIndex() {
		return l.snapChain
	}
	return l.ents[i-l.snapIndex-1].ChainSum
}

// at returns the entry at i in place. The caller guarantees
// snapIndex < i <= lastIndex; anything else panics on the slice bound.
func (l *memLog) at(i uint64) *core.EntryValue { return &l.ents[i-l.snapIndex-1] }

// matchTerm reports whether the log holds index i at term t: false for an
// index it does not hold, so a compacted or missing entry never matches.
func (l *memLog) matchTerm(i, t uint64) bool {
	x, ok := l.term(i)
	return ok && x == t
}

// slice returns the entries in [lo, hi), at least one, at most maxBytes,
// as a copy: a message queued over them is serialised after Ready, whose
// eviction blanks the log's own entries in place (#422).
func (l *memLog) slice(lo, hi uint64, maxBytes int) []core.EntryValue {
	if lo > hi || lo <= l.snapIndex {
		return nil
	}
	if hi > l.lastIndex()+1 {
		hi = l.lastIndex() + 1
	}
	src := l.ents[lo-l.snapIndex-1 : hi-l.snapIndex-1]
	n, size := 0, 0
	for n < len(src) {
		size += len(src[n].Data) + 64
		if n > 0 && size > maxBytes {
			break
		}
		n++
	}
	return slices.Clone(src[:n])
}

// resident sums the payload bytes held for (from, to].
func (l *memLog) resident(from, to uint64) int64 {
	var n int64
	for i := max(from, l.evicted, l.snapIndex) + 1; i <= to && i <= l.lastIndex(); i++ {
		n += int64(len(l.at(i).Data))
	}
	return n
}

// evict drops payloads, oldest first, while the window is above limit,
// never above through: only durable entries are dropped, committed ones
// handed to apply or not, which apply reads back from the store
// (ReadyValue.ApplyRead), and a lagging peer is fed by a fetch (#664).
func (l *memLog) evict(limit int64, through uint64) {
	if limit <= 0 {
		return
	}
	l.evicted = max(l.evicted, l.snapIndex)
	for l.window > limit && l.evicted < through && l.evicted < l.lastIndex() {
		e := l.at(l.evicted + 1)
		l.window -= int64(len(e.Data))
		e.Data = nil
		l.evicted++
	}
}

// truncateFrom drops every entry from index i on.
func (l *memLog) truncateFrom(i uint64) {
	if i <= l.lastIndex() {
		l.window -= l.resident(i-1, l.lastIndex())
	}
	if i <= l.snapIndex {
		l.ents = l.ents[:0]
		return
	}
	if i <= l.lastIndex() {
		l.ents = l.ents[:i-l.snapIndex-1]
	}
	if l.stable >= i {
		l.stable = i - 1
	}
}

// compactTo forgets the entries through i, which a snapshot now covers.
func (l *memLog) compactTo(i, t uint64, chain uint32) {
	if i <= l.snapIndex {
		return
	}
	l.window -= l.resident(l.snapIndex, min(i, l.lastIndex()))
	if i < l.lastIndex() {
		keep := l.ents[i-l.snapIndex:]
		l.ents = append(make([]core.EntryValue, 0, len(keep)), keep...)
	} else {
		l.ents = nil
	}
	l.snapIndex, l.snapTerm, l.snapChain = i, t, chain
	l.evicted = max(l.evicted, i)
	if l.window < 0 {
		l.window = 0
	}
	if l.stable < i {
		l.stable = i
	}
	if l.committed < i {
		l.committed = i
	}
	if l.applied < i {
		l.applied = i
	}
}
