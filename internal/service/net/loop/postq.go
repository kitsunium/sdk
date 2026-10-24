package loop

import (
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// postQueueCap is how many posted operations one loop holds before Post
// refuses with LoopPostQueueFull.
const postQueueCap int = 4096

// postQueue is a loop's preallocated queue of posted operations, the one
// door another goroutine has into a loop (ADR 0186 §D4).
//
// The protocol: a poster appends under mu, then moves pending from false to
// true; only the poster that wins that CAS wakes the loop, so a burst of posts
// costs one wake-up. The loop clears pending BEFORE it drains, so a post that
// lands after the drain began either is seen by it or wins the next CAS and
// wakes the loop again. Each operation is popped under mu, so it runs once.
type postQueue struct {
	// mu guards the ring below.
	mu sync.Mutex
	// ops and args are the ring's storage, allocated once.
	ops  []corenet.PostFunc
	args []uintptr
	// head is the next slot to pop; n how many are queued.
	head int
	n    int
	// pending is true from a winning post to the loop's next drain.
	pending atomic.Bool
	// closed refuses every post once the loop has stopped.
	closed bool
	// wake rouses the loop: an eventfd write on the native backends.
	wake func()
}

// newPostQueue allocates a queue of postQueueCap entries.
func newPostQueue(wake func()) *postQueue {
	//: allocated once, so a Post allocates nothing.
	return &postQueue{
		ops:  make([]corenet.PostFunc, postQueueCap),
		args: make([]uintptr, postQueueCap),
		wake: wake,
	}
}

// post queues op and wakes the loop when it is the first since its last
// drain.
func (q *postQueue) post(op corenet.PostFunc, arg uintptr) error {
	q.mu.Lock()
	//: a stopped loop runs nothing more.
	if q.closed {
		q.mu.Unlock()
		//: the loop is gone.
		return corenet.ServerClosed
	}
	//: the queue never grows.
	if q.n == len(q.ops) {
		q.mu.Unlock()
		//: refused, not queued.
		return corenet.LoopPostQueueFull
	}
	i := (q.head + q.n) % len(q.ops)
	q.ops[i], q.args[i] = op, arg
	q.n++
	q.mu.Unlock()
	//: one wake-up per drain, whatever the burst.
	if q.pending.CompareAndSwap(false, true) {
		q.wake()
	}
	//: queued.
	return nil
}

// drain runs every queued operation on the calling loop, in order. It clears
// pending first: see the type's comment.
func (q *postQueue) drain(io corenet.LoopIO) int {
	q.pending.Store(false)
	ran := 0
	//: popped one at a time, run outside the lock so an operation may post.
	for {
		q.mu.Lock()
		//: empty: done.
		if q.n == 0 {
			q.mu.Unlock()
			//: how many ran, for the caller's counters.
			return ran
		}
		op, arg := q.ops[q.head], q.args[q.head]
		q.ops[q.head] = nil
		q.head = (q.head + 1) % len(q.ops)
		q.n--
		q.mu.Unlock()
		op(io, arg)
		ran++
	}
}

// close refuses every later post. Operations already queued are dropped by
// the loop that stops.
func (q *postQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}
