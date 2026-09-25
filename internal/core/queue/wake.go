// Package queue — the ADR 0039 sibling that lets a consumer sleep until there
// may be work, and the value it answers with (ADR 0104).
package queue

import "time"

// Waker tells an idle consumer when to look at a broker again, so it can
// sleep until there may be work instead of polling on a fixed cadence.
//
// It is a SIBLING of [Broker] for the reason [DeadLetterReader] is: waking is
// a capability, and a broker over a transport that cannot see its own
// publications — or one whose state lives in another process — has no honest
// answer to give. The consumer engine asks by type assertion and polls when
// the answer is no, so an implementation that omits it loses latency, never
// correctness.
//
// A wake is a HINT, in both directions. A signal may arrive with nothing to
// receive — another consumer took it — and work may appear with no signal at
// all: a durable broker cannot see a publication made by another process. So
// a consumer that uses it still polls, at whatever cadence bounds the second
// case, and treats the first as a cheap empty Receive.
type Waker interface {
	// Wake returns what an idle consumer should wait on. Call it BEFORE the
	// Receive that may come back empty: a publication landing between that
	// Receive and the wait closes the channel already handed out, so it is
	// not missed.
	Wake() WakeValue
}

// WakeValue is what an idle consumer waits on: an event, or an instant the
// broker already knows about.
type WakeValue struct {
	// Signal is closed by the next event that may make a message receivable
	// through this broker in this process: a Publish, or a Nack that hands a
	// message back. It is never nil, and it is never closed by a publication
	// made in another process.
	Signal <-chan struct{}
	// In is how long, measured on the broker's own clock at the moment Wake
	// was called, until a message the broker already holds becomes
	// receivable with no further event: a retry delay ending, or a lease
	// lapsing because its consumer died. It is meaningful only when
	// Scheduled is true; zero or less means now.
	//
	// It is a duration and not an instant so that a consumer whose clock is
	// not the broker's still waits the right length of time rather than
	// comparing two clocks that disagree.
	In time.Duration
	// Scheduled reports that In names such a moment. It is false when the
	// broker holds nothing that will become receivable on its own.
	Scheduled bool
}
