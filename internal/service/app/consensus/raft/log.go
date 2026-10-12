package raft

import (
	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// memLog is the step function's view of the log: the snapshot base and every
// entry after it. The driver loads it at start and trims it after each
// compaction, so it holds no more than the store retains (D6).
type memLog struct {
	snapIndex uint64
	snapTerm  uint64
	snapChain uint32
	ents      []core.Entry
	committed uint64
	stable    uint64
	applied   uint64
}

func (l *memLog) lastIndex() uint64 { return l.snapIndex + uint64(len(l.ents)) }

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

func (l *memLog) at(i uint64) *core.Entry { return &l.ents[i-l.snapIndex-1] }

func (l *memLog) matchTerm(i, t uint64) bool {
	x, ok := l.term(i)
	return ok && x == t
}

// slice returns the entries in [lo, hi), at least one, at most maxBytes.
func (l *memLog) slice(lo, hi uint64, maxBytes int) []core.Entry {
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
	return src[:n:n]
}

// truncateFrom drops every entry from index i on.
func (l *memLog) truncateFrom(i uint64) {
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
	if i < l.lastIndex() {
		keep := l.ents[i-l.snapIndex:]
		l.ents = append(make([]core.Entry, 0, len(keep)), keep...)
	} else {
		l.ents = nil
	}
	l.snapIndex, l.snapTerm, l.snapChain = i, t, chain
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
