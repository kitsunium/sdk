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
	// takeLater waits for the next batch: no descriptor until a close.
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
}

// newListenerSet shares fds among takers takers.
func newListenerSet(fds []int, takers int) *listenerSet {
	ls := &listenerSet{fds: fds, left: make([]atomic.Int32, len(fds)), closed: make([]atomic.Bool, len(fds))}
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

// takeAfter says what an accept4 failure means for the drain's take.
func takeAfter(err error) uint8 {
	//: by errno.
	switch err {
	//: out of descriptors: the drain's closes make room.
	case syscall.EMFILE, syscall.ENFILE:
		//: next batch.
		return takeLater
	//: one connection lost, the queue still there.
	case syscall.ECONNABORTED, syscall.EINTR:
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
