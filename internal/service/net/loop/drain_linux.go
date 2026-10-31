//go:build linux

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
	// queueTakeBatch bounds what epoll's drain takes from one listener in one
	// batch, so the loop serves between two takes.
	queueTakeBatch int = 8 * epollAcceptBurst
)

// What an accept's failure means for epoll's drain take (takeAfter).
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

// newListenerSet shares fds among takers takers.
func newListenerSet(fds []int, takers int) *listenerSet {
	ls := &listenerSet{fds: fds, left: make([]atomic.Int32, len(fds)), closed: make([]atomic.Bool, len(fds)), accepts: make([]atomic.Uint64, len(fds))}
	//: every listener waits on every taker.
	for i := range ls.left {
		ls.left[i].Store(int32(takers))
	}
	//: shared.
	return ls
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

// takeAfter says what an accept4 failure means for the drain's take, live
// the connections the group still holds.
func takeAfter(err error, live int64) uint8 {
	//: by errno.
	switch err {
	//: out of descriptors: the drain's closes make room, unless the group has
	//: no connection left to close — nothing would ever free one.
	case syscall.EMFILE, syscall.ENFILE:
		//: none to close: over, as the portable backend gives up.
		if live == 0 {
			//: over.
			return takeOver
		}
		//: next batch.
		return takeLater
	//: one connection lost, the queue still there: the peer abandoned it, a
	//: signal, or a network error accept(2) says to retry on.
	case syscall.ECONNABORTED, syscall.EINTR, syscall.EPROTO, syscall.ENOPROTOOPT, syscall.EHOSTDOWN,
		syscall.ENONET, syscall.EHOSTUNREACH, syscall.EOPNOTSUPP, syscall.ENETUNREACH, syscall.ENETDOWN:
		//: the next one.
		return takeNext
	//: EAGAIN, the queue empty; anything else, the listener failed.
	default:
		//: over.
		return takeOver
	}
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

// closeSettled starts the drain's close of every connection of r the drain
// may close (drainable) that has no answer out with a worker (R3): each
// closes once its output has left; mid-request ones, and ones waiting on a
// worker, finish first.
func (lp *loop) closeSettled(r *ring) {
	//: every open connection is looked at.
	for s := r.lruHead; s != noLink; {
		c := &r.conns[s]
		nxt := c.lruNext
		//: settled, heard or quiet, and waiting on nobody.
		if r.drainable(c) && !lp.waits(r, s) {
			c.state, c.reason = stClosing, rDrain
			r.markDirty(s)
		}
		s = nxt
	}
}
