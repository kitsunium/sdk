// Package clock_test proves the one property this facade exists for: a time
// source written OUTSIDE internal/ can be named and built against the
// published port. That it is then accepted by every SDK configuration that
// takes one is asserted in pkg/v1/app/scheduler's suite: those configurations
// belong to the app, data and security families, which are modules above this
// one (ADR 0171), and a test here importing them would make clock require them.
//
// Every declaration below is written against pkg/v1 alone. Nothing here
// imports internal/kernel/clock, because the consumer this package was added
// for cannot — and a test that reached for the internal package would prove
// the opposite of what it claims (the precedent is pkg/v1/security/token, whose facade
// test hid two constructors no downstream module could call).
package clock_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// handTimer is a Timer written from outside the SDK. Returning it from
// NewTimer is only possible because clock.Timer is an alias: a locally
// declared interface with these three methods is a DIFFERENT named type, and
// Go compares method signatures by type identity.
type handTimer struct{ ch chan time.Time }

func (t *handTimer) C() <-chan time.Time      { return t.ch }
func (t *handTimer) Stop() bool               { return true }
func (t *handTimer) Reset(time.Duration) bool { return true }

// handTicker is the Ticker half of the same argument.
type handTicker struct{ ch chan time.Time }

func (t *handTicker) C() <-chan time.Time { return t.ch }
func (t *handTicker) Stop()               {}
func (t *handTicker) Reset(time.Duration) {}

// handClock is a complete clock.Timed — Now, Since, After, NewTimer, NewTicker
// and Sleep — written with no access to internal/. Before pkg/v1/clock existed
// this type could not be spelled at all: NewTicker had to return clock.Ticker,
// and clock.Ticker had no public name.
type handClock struct{ now time.Time }

func (c *handClock) Now() time.Time                  { return c.now }
func (c *handClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
func (c *handClock) Sleep(time.Duration)             {}

func (c *handClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

func (c *handClock) NewTimer(time.Duration) clock.Timer {
	return &handTimer{ch: make(chan time.Time, 1)}
}

func (c *handClock) NewTicker(time.Duration) clock.Ticker {
	return &handTicker{ch: make(chan time.Time, 1)}
}

// readOnlyClock is the two-method half of the port. clock.Clock is frozen at
// Now and Since, so this keeps satisfying every field typed that way.
type readOnlyClock struct{ now time.Time }

func (c readOnlyClock) Now() time.Time                  { return c.now }
func (c readOnlyClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

// Compile-time assertions that the two doubles satisfy the published port.
// These are the whole point of the package, so they fail the build rather than
// a test run.
var (
	_ clock.Clock  = (*handClock)(nil)
	_ clock.Waiter = (*handClock)(nil)
	_ clock.Timed  = (*handClock)(nil)
	_ clock.Clock  = readOnlyClock{}
	_ clock.Timer  = (*handTimer)(nil)
	_ clock.Ticker = (*handTicker)(nil)

	//: the production value satisfies both halves of the port, and naming the
	//: types here is itself the thing this package exists to make possible.
	_ clock.Timed  = clock.System
	_ clock.Clock  = clock.System
	_ clock.Waiter = clock.System
)

// TestSystemIsReferenceableAndIsATimed pins the production value's presence and
// its type. It is what every Clock field falls back to when left nil, so a
// consumer naming it explicitly beside a test double must be able to.
func TestSystemIsReferenceableAndIsATimed(t *testing.T) {
	t.Parallel()

	//: System is DECLARED clock.Timed — typed as the union rather than the
	//: reading half so a caller that needs to wait does not type-assert — so
	//: inference already yields the port type and no conversion is written. The
	//: package-level block pins the three assignabilities at compile time; this
	//: asserts the value itself is usable.
	timed := clock.System
	if timed == nil {
		t.Fatal("clock.System is nil")
	}

	//: reading through the frozen two-method half, which is what most SDK
	//: configurations ask for.
	reading := clock.Clock(clock.System)
	if reading.Since(reading.Now()) < 0 {
		t.Fatal("clock.System reported a negative elapsed duration")
	}
}
