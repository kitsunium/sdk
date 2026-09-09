// Package clock — hosts the [Ticker] contract, the handle a [Waiter] returns
// for a repeating wake-up.
package clock

import "time"

// Ticker is a repeating wake-up scheduled on some clock. It mirrors the usable
// surface of *time.Ticker — including its drop-on-slow-receiver contract: the
// delivery channel holds at most one pending tick, and ticks that cannot be
// delivered are discarded rather than queued.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type Ticker interface {
	// C returns the channel on which each tick instant is delivered. The same
	// channel is returned by every call; it is never closed.
	C() <-chan time.Time
	// Stop halts the Ticker. It does NOT close the channel and does NOT
	// discard an already-delivered tick, so a concurrent receiver never sees a
	// spurious zero value — the same guarantee time.Ticker.Stop makes.
	Stop()
	// Reset halts the Ticker and restarts it with period d. It PANICS on a
	// non-positive d; see the package comment.
	Reset(d time.Duration)
}
