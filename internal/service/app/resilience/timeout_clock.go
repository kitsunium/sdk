package resilience

import (
	"context"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// clockDeadline is a context that expires when an injected clock passes its
// deadline, rather than when the wall clock does. It is what NewTimeoutWith
// hands the Operation under any clock but the system one.
//
// It is a context of its own, not a context.WithCancelCause, on purpose: a
// cancelCtx reports context.Canceled from Err however it was cancelled, and so
// does every context derived from it, so an Operation checking
// errors.Is(ctx.Err(), context.DeadlineExceeded) would never see the deadline.
// With its own Done channel, a context derived from this one is propagated
// through Err and reports DeadlineExceeded too.
//
// Deadline reports the caller's deadline, never this one: a time on a
// ManualClock is not a time a socket can be given.
type clockDeadline struct {
	parent    context.Context
	done      chan struct{}
	closeDone sync.Once
	mu        sync.Mutex
	err       error
}

// newClockDeadline returns a context that expires after d on clk, or when
// parent is done, and the function that releases it. The watcher goroutine
// ends at whichever comes first, or at release.
func newClockDeadline(parent context.Context, clk clock.Timed, d time.Duration) (context.Context, context.CancelFunc) {
	c := &clockDeadline{parent: parent, done: make(chan struct{})}
	//: a parent already finished is reported at once, not after the watcher
	//: runs, so the Operation never sees a live context under a dead caller.
	if parentErr := parent.Err(); parentErr != nil {
		//: nothing to watch.
		c.finish(parentErr)
		//: release is a no-op on a finished context.
		return c, func() {}
	}
	timer := clk.NewTimer(d)
	release := make(chan struct{})
	go c.watch(timer, release)
	var once sync.Once
	//: release may be called more than once, as a CancelFunc may.
	return c, func() { once.Do(func() { close(release) }) }
}

// watch finishes the context at the first of the deadline, the parent's end
// and the release, then stops the timer so a ManualClock holds no waiter.
func (c *clockDeadline) watch(timer clock.Timer, release <-chan struct{}) {
	//: the timer is the clock's resource; stop it whatever ended the wait.
	defer timer.Stop()
	select {
	case <-timer.C():
		//: the injected clock passed the deadline.
		c.finish(context.DeadlineExceeded)
	case <-c.parent.Done():
		//: the caller's context ended first — its error is the one reported.
		c.finish(c.parent.Err())
	case <-release:
		//: the Operation returned; the context is released as a cancel does.
		c.finish(context.Canceled)
	}
}

// finish records err as the context's error and closes Done, once: the first
// cause to arrive wins and Err never changes afterwards.
func (c *clockDeadline) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: already finished — the first error stands.
	if c.err != nil {
		//: Err is stable once non-nil.
		return
	}
	c.err = err
	//: under the lock, so Done is closed by the time another reader sees err;
	//: the Once states what the err check above already guarantees.
	c.closeDone.Do(func() { close(c.done) })
}

// Deadline reports the caller's deadline: this context's own runs on a clock
// that is not the wall clock.
func (c *clockDeadline) Deadline() (time.Time, bool) {
	//: the only wall-clock deadline there is.
	return c.parent.Deadline()
}

// Done is closed once the context is finished.
func (c *clockDeadline) Done() <-chan struct{} {
	//: closed by finish.
	return c.done
}

// Err reports why the context finished, or nil while it has not. A parent
// that has finished is folded in on the spot rather than when the watcher
// notices it, so Err never reports a live context under a dead caller.
func (c *clockDeadline) Err() error {
	//: the parent is checked first, outside the lock, as finish takes it.
	if parentErr := c.parent.Err(); parentErr != nil {
		//: records it unless the deadline was reached first.
		c.finish(parentErr)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	//: nil until finished, then stable.
	return c.err
}

// Value is the parent's: this context carries no value of its own.
func (c *clockDeadline) Value(key any) any {
	//: values flow through unchanged.
	return c.parent.Value(key)
}
