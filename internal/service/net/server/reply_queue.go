package server

import (
	stdnet "net"
	"sync"
)

// queuedReply is one reply queuedSink holds: where its bytes sit in the arena
// and the peer it goes to.
type queuedReply struct {
	// off and size place the reply's bytes in the arena.
	off, size int
	// to is the peer.
	to stdnet.Addr
}

// queuedSink is BatchReplies where sendmmsg is not: the batch's replies are
// queued and leave at its end, one WriteTo each (ADR 0192). The option's
// contract — a reply reported sent when queued, a failure counted — is the
// same on every platform; only the syscall count differs.
//
// Its queue and arena are allocated once, at construction; a mutex guards
// them, since a handler that kept its Packet may reply from another goroutine.
type queuedSink struct {
	// pc is the socket replies leave through.
	pc stdnet.PacketConn
	// counters is where failed replies are counted.
	counters *datagramCounters
	// mu guards everything below.
	mu sync.Mutex
	// open is true between begin and flush, while replies are queued.
	open bool
	// arena holds the queued replies' bytes, used of it so far.
	arena []byte
	used  int
	// queue is the replies queued in this batch.
	queue []queuedReply
}

// newQueuedSink builds a queued sink over pc.
func newQueuedSink(pc stdnet.PacketConn, counters *datagramCounters) *queuedSink {
	//: sized once, so a reply allocates nothing.
	return &queuedSink{
		pc: pc, counters: counters,
		arena: make([]byte, replyArena), queue: make([]queuedReply, 0, replyQueue),
	}
}

// begin implements datagramSink: replies are queued from here.
func (w *queuedSink) begin() {
	w.mu.Lock()
	w.open = true
	w.mu.Unlock()
}

// reply implements datagramSink: queued inside a batch, at once outside one or
// when it is larger than the arena.
func (w *queuedSink) reply(b []byte, to stdnet.Addr) (n int, err error) {
	w.mu.Lock()
	//: no batch open, or a reply the arena cannot hold: at once.
	if !w.open || len(b) > len(w.arena) {
		w.mu.Unlock()
		//: the write's own answer.
		return w.pc.WriteTo(b, to)
	}
	//: the queue or the arena is full: send what is queued to make room.
	if len(w.queue) == cap(w.queue) || w.used+len(b) > len(w.arena) {
		w.flushLocked()
	}
	w.queue = append(w.queue, queuedReply{off: w.used, size: len(b), to: to})
	copy(w.arena[w.used:], b)
	w.used += len(b)
	w.mu.Unlock()
	//: queued: it leaves at the batch's end.
	return len(b), nil
}

// flush implements datagramSink: it sends what the batch queued and closes it.
func (w *queuedSink) flush() {
	w.mu.Lock()
	w.flushLocked()
	w.open = false
	w.mu.Unlock()
}

// flushLocked sends the queue, one WriteTo a reply, and empties it. mu is held.
func (w *queuedSink) flushLocked() {
	//: in the order they were made.
	for i := range w.queue {
		r := &w.queue[i]
		//: the handler has returned: a failure is counted, not reported.
		if _, err := w.pc.WriteTo(w.arena[r.off:r.off+r.size], r.to); err != nil {
			w.counters.replyFailures.Add(1)
		}
		r.to = nil
	}
	w.queue = w.queue[:0]
	w.used = 0
}
