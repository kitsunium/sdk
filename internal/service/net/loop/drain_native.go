//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// drainQuiet is how long the drain leaves open a connection that has sent
	// nothing yet. One accepted as the drain began has its request on the way:
	// closed before it is read, the request would meet a closed socket and the
	// peer a reset. One that stays silent that long is closed like any other.
	drainQuiet time.Duration = time.Second
	// drainQuietTicks is drainQuiet in ticks.
	drainQuietTicks int64 = int64(drainQuiet / tickEvery)
	// queueTakeMax bounds how many connections the drain takes from one
	// listener's queue, a loop on epoll, a ring on io_uring: past it the
	// arrivals outpace the drain — no grace let the routing stop —, and what
	// is left in the queue meets the close.
	queueTakeMax int = 1 << 16
	// acceptBurst is the most connections one listener readiness accepts, on
	// epoll and on kqueue, before the loop serves the events already in hand.
	acceptBurst int = 128
	// queueTakeBatch bounds what the drain of epoll or kqueue takes from one
	// listener in one batch, so the loop serves between two takes.
	queueTakeBatch int = 8 * acceptBurst
)

// What an accept's failure means for the drain take of epoll and kqueue
// (takeAfter).
const (
	// takeOver ends the take: the queue is empty, or the listener failed.
	takeOver uint8 = iota
	// takeLater waits for the next batch: no descriptor until a close, with
	// a connection of the group left whose close frees one.
	takeLater
	// takeNext skips one connection: the peer abandoned it, or a signal.
	takeNext
)

// listenerSet is a group's listeners as its loops share them through the
// drain (plan P6). Each taker — a loop on epoll, a ring on io_uring — takes
// its share of a listener's queue once its own accepts are over, then hands
// the listener back; the last one closes it, so the listener closes before
// the established connections drain, and never while a taker may still
// accept on it. Whatever no taker closed — a group stopped, not drained —
// closes once every loop returned.
type listenerSet struct {
	// fds are the listeners, in the order of Config.Listeners.
	fds []int
	// left is how many takers have still to hand each listener back.
	left []atomic.Int32
	// closed is set once a listener is closed, by its last taker or by
	// closeRest.
	closed []atomic.Bool
	// accepts counts each listener's accepted connections, every taker
	// together: io_uring's watchdog reads in it that a listener's queue
	// moves, whichever ring took the connections (ADR 0200 §D1).
	accepts []atomic.Uint64
}

// newListenerSet shares fds among their takers: takersOf(i) of them hand
// listener i back.
func newListenerSet(fds []int, takersOf func(i int) int) *listenerSet {
	ls := &listenerSet{fds: fds, left: make([]atomic.Int32, len(fds)), closed: make([]atomic.Bool, len(fds)), accepts: make([]atomic.Uint64, len(fds))}
	//: every listener waits on every one of its takers.
	for i := range ls.left {
		ls.left[i].Store(int32(takersOf(i)))
	}
	//: shared.
	return ls
}

// takersOfShards counts the takers of a group whose listeners may each belong
// to one loop: a shard is taken by that loop's rings, a shared listener by
// every loop's.
func takersOfShards(owners []int, loops, rings int) func(int) int {
	//: by the listener's owner.
	return func(i int) int {
		//: one loop's rings, or all of them.
		if owners[i] != 0 {
			//: its one loop.
			return rings
		}
		return loops * rings
	}
}

// ownersOf is cfg's listeners' owners, in Config.Listeners' order.
func ownersOf(ls []Listener) []int {
	out := make([]int, len(ls))
	//: each one.
	for i, l := range ls {
		out[i] = l.Loop
	}
	//: aligned with the listeners.
	return out
}

// ownedBy reports whether loop index accepts on listener i: its shard, or a
// listener every loop shares.
func ownedBy(owners []int, i, index int) bool {
	//: shared, or its owner.
	return owners[i] == 0 || owners[i] == index+1
}

// handBack hands listener i back: its taker will accept on it no more. The
// last taker closes it.
func (ls *listenerSet) handBack(i int) {
	//: another taker still accepts on it.
	if ls.left[i].Add(-1) != 0 {
		//: not yet.
		return
	}
	ls.close(i)
}

// accepted counts a connection accepted on listener i.
func (ls *listenerSet) accepted(i int) {
	//: one more, whoever took it.
	ls.accepts[i].Add(1)
}

// acceptsOf is how many connections listener i gave so far, every taker
// together.
func (ls *listenerSet) acceptsOf(i int) uint64 {
	//: the shared count.
	return ls.accepts[i].Load()
}

// closeRest closes every listener no taker closed.
func (ls *listenerSet) closeRest() {
	//: each one, once.
	for i := range ls.fds {
		ls.close(i)
	}
}

// close closes listener i once.
func (ls *listenerSet) close(i int) {
	//: already closed.
	if !ls.closed[i].CompareAndSwap(false, true) {
		//: once.
		return
	}
	swallowErr(syscall.Close(ls.fds[i]))
}

// doneOutside is a taker's queueDone at the start: a shard of another loop
// was never this one's to take, so it starts handed back (#856).
func doneOutside(owners []int, index int) []bool {
	done := make([]bool, len(owners))
	//: each listener.
	for i := range owners {
		done[i] = !ownedBy(owners, i, index)
	}
	//: true for another loop's shards.
	return done
}

// full reports whether the ring's table has no slot left for a connection.
func (r *ring) full() bool {
	//: no released slot, and the table at its size.
	return len(r.free) == 0 && len(r.conns) >= r.lp.sz.tableSize
}

// drainable reports whether the drain closes the open connection c now: no
// partial input, and either it has sent something — its request was read —
// or it stayed silent for drainQuiet.
func (r *ring) drainable(c *connCtl) bool {
	//: mid-request connections finish first; fresh ones get their request in.
	return c.state == stOpen && c.spill.empty() && (c.flags&fHeard != 0 || r.lp.tick-c.lastIO >= drainQuietTicks)
}

// settleDrain is the drain's look at the open connections, once
// OnBatchEnd has returned and before endBatch flushes: whatever the batch's
// requests asked for is written by then, the handlers answering at
// OnBatchEnd included, so a close the look marks leaves after it — a
// closing slot finishes only once its output has left (settle) — and no
// answer is refused (#1385). A connection fed in every batch is judged at
// every look, so it hears OnDrain at one and closes at the next.
func (lp *loop) settleDrain() {
	//: not draining: nothing to look at.
	if !lp.draining {
		//: serving.
		return
	}
	//: every ring.
	for _, r := range lp.rings {
		lp.closeSettled(r)
	}
}

// closeSettled starts the drain's close of every connection of r the drain
// may close (drainable) that has no answer out with a worker (R3): each
// closes once its output has left; mid-request ones, and ones waiting on a
// worker, finish first. It runs after OnBatchEnd (settleDrain).
func (lp *loop) closeSettled(r *ring) {
	//: every open connection is looked at.
	for s := r.lruHead; s != noLink; {
		c := &r.conns[s]
		nxt := c.lruNext
		//: settled, heard or quiet, and waiting on nobody.
		if r.drainable(c) && !lp.waits(r, s) {
			//: the handler's last word, once; the connection closes next batch.
			if lp.drainer != nil && c.flags&fDrained == 0 {
				c.flags |= fDrained
				lp.drainer.OnDrain(lp, r.ref(s))
				s = nxt
				continue
			}
			c.state, c.reason = stClosing, rDrain
			r.markDirty(s)
		}
		s = nxt
	}
}
