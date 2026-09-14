// Package clock — hosts the production implementation of the time port:
// systemClock, a zero-sized value delegating every method to package time, and
// the [System] singleton every consumer defaults to.
package clock

import "time"

// System is the default Timed using the package time wall clock. It is typed
// [Timed] rather than [Clock] so callers that need to wait do not have to
// type-assert; assigning it to a Clock field still compiles unchanged.
var System Timed = systemClock{}

// systemClock is the default Timed backed by the package time wall clock.
type systemClock struct{}

// Now returns the current wall-clock instant from package time.
func (systemClock) Now() time.Time {
	//: delegate to the standard library so the OS provides the timestamp.
	return time.Now()
}

// Since returns the elapsed duration between t and the current wall-clock time.
func (systemClock) Since(t time.Time) time.Duration {
	//: delegate to the standard library for monotonic-aware subtraction.
	return time.Since(t)
}

// After returns a channel delivering the instant d from now.
func (systemClock) After(d time.Duration) <-chan time.Time {
	//: time.After already treats a non-positive d as "fire now".
	return time.After(d)
}

// NewTimer returns a one-shot Timer due d from now.
func (systemClock) NewTimer(d time.Duration) Timer {
	//: the wrapper exists only to turn the stdlib's C FIELD into a method;
	//: it holds one pointer, so boxing it into the interface allocates nothing
	//: — pinned by BenchmarkStdlib_NewTimer vs BenchmarkSystem_NewTimer.
	return systemTimer{tm: time.NewTimer(d)}
}

// NewTicker returns a repeating Ticker of period d, panicking on a
// non-positive d.
func (systemClock) NewTicker(d time.Duration) Ticker {
	//: refuse here rather than letting time.NewTicker's own panic escape, so
	//: System and ManualClock reject the same input with the same message.
	requirePositivePeriod("NewTicker", d)
	//: same one-pointer wrapper rationale as NewTimer.
	return systemTicker{tk: time.NewTicker(d)}
}

// Sleep blocks the calling goroutine for d.
func (systemClock) Sleep(d time.Duration) {
	//: time.Sleep already returns at once for a non-positive d.
	time.Sleep(d)
}

// systemTicker adapts *time.Ticker to the [Ticker] interface. Same one-pointer
// shape as systemTimer, and the same allocation-free boxing.
type systemTicker struct {
	// tk is the wrapped stdlib ticker; never nil once constructed.
	tk *time.Ticker
}

// C returns the stdlib ticker's delivery channel.
func (t systemTicker) C() <-chan time.Time {
	//: expose the stdlib field as the method the interface requires.
	return t.tk.C
}

// Stop halts the ticker without closing its channel.
func (t systemTicker) Stop() {
	//: delegate; since Go 1.23 the runtime itself guarantees that a tick
	//: nobody received before Stop is never received after it.
	t.tk.Stop()
}

// Reset restarts the ticker with period d, panicking on a non-positive d.
func (t systemTicker) Reset(d time.Duration) {
	//: own the refusal so the message matches ManualClock's exactly.
	requirePositivePeriod("Ticker.Reset", d)
	//: delegate the re-arming to the stdlib ticker.
	t.tk.Reset(d)
}

// systemTimer adapts *time.Timer to the [Timer] interface. It is a value type
// holding exactly one pointer, so converting it to Timer is allocation-free
// (the runtime stores a pointer-shaped value directly in the interface word).
type systemTimer struct {
	// tm is the wrapped stdlib timer; never nil once constructed.
	tm *time.Timer
}

// C returns the stdlib timer's delivery channel.
func (t systemTimer) C() <-chan time.Time {
	//: expose the stdlib field as the method the interface requires.
	return t.tm.C
}

// Stop halts the timer, reporting whether it was still armed.
func (t systemTimer) Stop() bool {
	//: since Go 1.23 the channel is unbuffered, so Stop leaves nothing stale.
	return t.tm.Stop()
}

// Reset re-arms the timer for d, reporting whether it was still armed.
func (t systemTimer) Reset(d time.Duration) bool {
	//: delegate; the stdlib guarantees no stale value survives the reset.
	return t.tm.Reset(d)
}
