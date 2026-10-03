// Package lock — one live acquisition of one name, in the in-process locker.
package lock

import (
	"sync"
	"time"
)

// holding is one live acquisition of one name.
type holding struct {
	// token identifies the acquisition, not the name. Two acquisitions of the
	// same name never share one, which is what lets Release refuse to unlock a
	// section that now belongs to someone else.
	token uint64
	// fence is the acquisition's fencing token — the value handed to the
	// caller and, if the caller plumbs it through, to the protected resource.
	fence uint64
	// deadline is when this holding stops being held unless it is renewed.
	deadline time.Time
	// free is closed when this holding ends, by release or by takeover. It
	// wakes every waiter at once so they re-contend; a waiter is never handed
	// the lock directly, because a hand-off would have to decide fairness and
	// any order chosen here would be invisible to the caller.
	free chan struct{}
	// ended guards the close of free.
	//
	// Every caller of [holding.end] already holds the locker's mutex and the
	// map invariants make a second close unreachable — but "unreachable given
	// an invariant elsewhere" is exactly the reasoning that turns a refactor
	// into a panic in production. The Once costs one atomic on a path that
	// already took a mutex.
	ended sync.Once
}

// end closes free exactly once, waking every waiter on this acquisition.
func (h *holding) end() {
	//: idempotent by construction — see the field comment for why that is not
	//: redundant with the mutex the caller already holds.
	h.ended.Do(func() {
		//: one close, whichever path got here first.
		close(h.free)
	})
}
