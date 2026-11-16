package raft

import (
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The forwards a node received (#401, #1482). Two things are kept apart:
// the answers given, which answer a copy of a forward for the forward
// window, a precision; and the fence, which no time bounds and on which the
// safety rests: a copy whose answer is gone is neither proposed nor refused
// again, whatever its delay, across this node's restarts too.
const (
	// maxFwdAnswers bounds the answers a node keeps of the forwards it
	// received, 152 MiB once that many are kept (152 B an answer, measured
	// on amd64). At the bound the oldest answer is forgotten, one for each
	// new answer kept, so no forward is ever dropped for room (the owner's
	// decision of 2026-11-16, #1482): the fence (fwdFence) refuses a copy of
	// the answer forgotten, its forwarder answering OUTCOME_UNKNOWN. Under
	// the default window the bound is reached at 47 662 forwards a second,
	// and a table holds what its load keeps: rate × window answers.
	maxFwdAnswers int = 1 << 20
	// fwdBlock is how many answers one block of the table's queue holds:
	// the queue grows and shrinks by whole blocks, never by moving what it
	// keeps.
	fwdBlock int = 512
	// minFwdRebuild is how many answers the table's map must have held for
	// it to be rebuilt once a quarter of them is left: a map never gives
	// back the room it grew to.
	minFwdRebuild int = 4096
	// fwdRebuildStep is how many answers a rebuild moves to the new map per
	// Tick or forward, so no turn of the loop rebuilds a large map at once:
	// rebuilt in one turn, 75 000 answers took 23.5 ms, past a lease's
	// drift budget (#1482, review O4).
	fwdRebuildStep int = 1024
	// fwdExpireStep is the most answers one Tick or forward forgets at the
	// window's end: a node back from a pause ages a second of answers at
	// once, at 47 662 forwards a second as many, and an answer kept past
	// its window answers its copy as it did within it.
	fwdExpireStep int = 4096
	// defaultForwardWindow is the forward window when the driver gives
	// none (Config.ForwardWindow): twice the shipped TCP transport's
	// resend window under its defaults on Linux, 5 × 2 s + 1 s.
	defaultForwardWindow time.Duration = 22 * time.Second
	// maxForwardWindow bounds the forward window whatever the driver gives:
	// the window is a precision, the fence keeps a copy past it from being
	// handled again, and a window that grew with an unbounded WriteTimeout
	// let a few forwards a second reach the table's bound (#1482, review
	// O2).
	maxForwardWindow time.Duration = 2 * time.Minute
	// fwdClockStep is the most the table's clock moves between two
	// readings of the driver's clock: a longer gap is time this node did
	// not run, a paused machine or process, while the copies its peers
	// resent waited in its sockets.
	fwdClockStep time.Duration = time.Second
)

// fwdKey names one submission: the follower that forwarded it and its
// submission id, which never repeats across the follower's restarts (R7-23).
// Two frames with one key are one submission delivered twice.
type fwdKey struct {
	from core.NodeID
	ctx  uint64
}

// fwdAnswer is what a forward was answered: the position its entry was
// appended at, or a refusal with its code and the leader hinted.
type fwdAnswer struct {
	reject  bool
	code    errs.Code
	hint    uint64
	index   uint64
	logTerm uint64
	// commit is the leader's commit when it answered a session command:
	// a position at or below it was committed (#918).
	commit uint64
}

// fwdStamp is a kept answer's key and the table's clock when it was given.
type fwdStamp struct {
	key fwdKey
	at  time.Duration
}

// fwdTable keeps the answers given to forwards for the forward window, on
// its own clock, and at most a bound of them, the oldest forgotten first
// (#1482). It outlives a term:
// a duplicate that arrives after this node changed term, or led again, is
// still the submission it answered, and replaying an old answer is
// harmless, the follower having taken the first one (#401). It is volatile,
// and forgetting an answer is safe: the fence (fwdFence) keeps a copy that
// comes later from being handled again.
type fwdTable struct {
	byKey map[fwdKey]fwdAnswer
	// next, while not nil, is the map being rebuilt a step at a time: it
	// takes every new answer, and those of byKey are moved into it, the
	// youngest first, until none is left (rebuildStep).
	next map[fwdKey]fwdAnswer
	// blocks hold the kept keys in the order they were answered, each with
	// the clock it was answered at, from blocks[0][head] on; every block
	// but the last holds fwdBlock of them.
	blocks [][]fwdStamp
	head   int
	// front is the sequence number of the oldest kept answer, the count of
	// answers forgotten so far; size is how many are kept.
	front uint64
	size  int
	// moveAt, while next is not nil, bounds the answers of byKey not moved
	// yet: those of sequence number from front up to it.
	moveAt uint64
	// peak is the most answers kept since the map was last rebuilt.
	peak int
	// clock is the time this node ran since it started, as the driver's
	// clock read at each Step and Tick tells it; seen is the latest
	// reading.
	clock time.Duration
	seen  time.Time
}

// advance moves the table's clock to now, a gap longer than step counted as
// step, and a reading earlier than the latest not at all, so the clock
// never runs faster than the driver's.
func (a *fwdTable) advance(now time.Time, step time.Duration) {
	if !now.After(a.seen) {
		return
	}
	if !a.seen.IsZero() {
		a.clock += min(now.Sub(a.seen), step)
	}
	a.seen = now
}

// lookup returns the answer kept for k.
func (a *fwdTable) lookup(k fwdKey) (fwdAnswer, bool) {
	if v, ok := a.next[k]; ok {
		return v, true
	}
	v, ok := a.byKey[k]
	return v, ok
}

// kept is how many answers the table keeps.
func (a *fwdTable) kept() int {
	return a.size
}

// full reports whether limit answers are kept.
func (a *fwdTable) full(limit int) bool {
	return a.size >= limit
}

// makeRoom forgets the oldest answer kept, whatever its age, when limit
// answers are kept, and reports whether it did: called before each answer
// remembered, it forgets one answer for one kept, never more.
func (a *fwdTable) makeRoom(limit int) bool {
	if a.size == 0 || !a.full(limit) {
		return false
	}
	a.forgetOldest()
	return true
}

// remember keeps v for k at the table's clock; makeRoom bounds the table.
func (a *fwdTable) remember(k fwdKey, v fwdAnswer) {
	a.keep(k)[k] = v
}

// forgetOldest forgets the oldest answer kept, whatever its age.
func (a *fwdTable) forgetOldest() {
	st := a.blocks[0][a.head]
	delete(a.byKey, st.key)
	delete(a.next, st.key)
	a.blocks[0][a.head] = fwdStamp{}
	a.head++
	a.front++
	a.size--
	if a.head == fwdBlock {
		// A block is given back as soon as its last answer goes.
		a.blocks[0] = nil
		a.blocks = a.blocks[1:]
		a.head = 0
	}
}

// keep queues k's stamp at the table's clock and returns the map its
// answer goes in: the one being rebuilt while there is one.
func (a *fwdTable) keep(k fwdKey) map[fwdKey]fwdAnswer {
	if n := len(a.blocks); n == 0 || len(a.blocks[n-1]) == fwdBlock {
		a.blocks = append(a.blocks, make([]fwdStamp, 0, fwdBlock))
	}
	last := len(a.blocks) - 1
	a.blocks[last] = append(a.blocks[last], fwdStamp{key: k, at: a.clock})
	a.size++
	a.peak = max(a.peak, a.size)
	switch {
	case a.next != nil:
		return a.next
	case a.byKey == nil:
		a.byKey = map[fwdKey]fwdAnswer{}
	}
	return a.byKey
}

// stamp is the kept answer of sequence number s, front <= s < front+size.
func (a *fwdTable) stamp(s uint64) fwdStamp {
	p := a.head + int(s-a.front)
	return a.blocks[p/fwdBlock][p%fwdBlock]
}

// expire forgets the answers kept for window or longer, the oldest first
// and fwdExpireStep at most, then moves the map's rebuild a step on.
func (a *fwdTable) expire(window time.Duration) {
	for n := 0; a.size > 0 && n < fwdExpireStep; n++ {
		if a.clock-a.blocks[0][a.head].at < window {
			break
		}
		a.forgetOldest()
	}
	if a.size == 0 {
		clear(a.blocks)
		a.blocks, a.head = a.blocks[:0], 0
	}
	a.rebuildStep()
}

// rebuildStep gives the map's room back, fwdRebuildStep answers per call:
// once the map held minFwdRebuild answers and a quarter of its peak is
// left, a new map takes every new answer and the old one's are moved into
// it, the youngest first since the oldest expire first; once none is left
// the old map goes.
func (a *fwdTable) rebuildStep() {
	if a.next == nil {
		if a.peak < minFwdRebuild || a.size > a.peak/4 {
			return
		}
		a.next = make(map[fwdKey]fwdAnswer, min(a.size, fwdRebuildStep))
		a.moveAt = a.front + uint64(a.size)
	}
	lo := max(a.front, a.moveAt-min(a.moveAt, uint64(fwdRebuildStep)))
	for s := lo; s < a.moveAt; s++ {
		k := a.stamp(s).key
		if _, moved := a.next[k]; moved {
			continue
		}
		if v, ok := a.byKey[k]; ok {
			a.next[k] = v
		}
	}
	a.moveAt = lo
	if a.moveAt <= a.front {
		a.byKey, a.next, a.peak = a.next, nil, a.size
	}
}
