package mailkit

import (
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// sdkClock is the app's clock, as kit's core hands it (plug.Clock), as the
// SDK's engine stamps and waits on it: the same instants and the same
// timers, a test's manual clock included.
type sdkClock struct {
	plug.Clock
}

// clockOf is c as the SDK's spool takes it, nil — the spool's own, the
// system's — for none.
func clockOf(c plug.Clock) clock.Timed {
	if c == nil {
		return nil
	}
	return sdkClock{Clock: c}
}

// NewTimer is the clock's timer, as the SDK's.
func (c sdkClock) NewTimer(d time.Duration) clock.Timer { return c.Clock.NewTimer(d) }

// NewTicker is the clock's ticker, as the SDK's.
func (c sdkClock) NewTicker(d time.Duration) clock.Ticker { return c.Clock.NewTicker(d) }
