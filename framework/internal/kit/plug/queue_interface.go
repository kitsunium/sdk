package plug

import (
	"context"
	"time"
)

// The queue subsystem's broker and its sibling (ADR 0179 §3, step 4), which
// framework/internal/kit/queuekit implements over the SDK's queue, and the
// app's clock as a queue stamps and waits on it, which the core implements.

// Broker is one queue as kit's core uses it: a publication, the consumer
// that delivers it, and its dead letters. It grows by the sibling below.
type Broker interface {
	// Publish puts payload in the queue: in the transaction ctx carries for
	// its database, when it lives on one.
	Publish(ctx context.Context, payload []byte) error
	// Consume delivers the queue's messages to cfg's handler until ctx ends
	// or the queue's storage fails.
	Consume(ctx context.Context, cfg ConsumeConfig) error
	// DeadLetters are up to maxLetters of the queue's dead letters, oldest
	// first.
	DeadLetters(ctx context.Context, maxLetters int) ([]DeadLetterValue, error)
}

// MessageMover is a Broker whose messages a data copy reads and writes as
// they lie, without a lease, with their counts and their instants. A
// sibling of [Broker].
type MessageMover interface {
	// Messages are up to maxMessages live messages after the identifier
	// after, "" for the first, in their queue's order.
	Messages(ctx context.Context, after string, maxMessages int) ([]LiveMessageValue, error)
	// PutMessage writes live under an identifier of the queue's own, and
	// returns it.
	PutMessage(ctx context.Context, live LiveMessageValue) (string, error)
	// PutDeadLetter writes dead, with its cause, under an identifier of the
	// queue's own, and returns it.
	PutDeadLetter(ctx context.Context, dead DeadLetterValue) (string, error)
}

// Clock is the app's clock as a queue stamps and waits on it: kit's core
// hands the one its app runs on, a test's manual clock included.
type Clock interface {
	// Now is the clock's instant.
	Now() time.Time
	// Since is how long ago t was, on the clock.
	Since(t time.Time) time.Duration
	// After is a channel that receives the clock's instant once d passed.
	After(d time.Duration) <-chan time.Time
	// NewTimer is a timer that fires once d passed.
	NewTimer(d time.Duration) Timer
	// NewTicker is a ticker that fires every d.
	NewTicker(d time.Duration) Ticker
	// Sleep waits until d passed.
	Sleep(d time.Duration)
}

// Timer is a timer of a [Clock].
type Timer interface {
	// C receives the clock's instant when the timer fires.
	C() <-chan time.Time
	// Stop stops the timer, and reports whether it had not fired.
	Stop() bool
	// Reset makes the timer fire once d passed, and reports whether it had
	// not fired.
	Reset(d time.Duration) bool
}

// Ticker is a ticker of a [Clock].
type Ticker interface {
	// C receives the clock's instant at each tick.
	C() <-chan time.Time
	// Stop stops the ticker.
	Stop()
	// Reset makes the ticker fire every d.
	Reset(d time.Duration)
}
