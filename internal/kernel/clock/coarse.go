package clock

import (
	"sync"
	"sync/atomic"
	"time"
)

// CoarseClock is a [Clock] whose Now is one atomic load: a goroutine of its
// own re-reads its base clock every resolution and publishes the instant,
// so a reading is at most one resolution behind the base. It trades
// precision for cost on a hot path — a TTL cache hit that reads the time on
// every lookup — and makes every deadline it measures late by up to the
// resolution.
//
// Stop ends the goroutine; a stopped CoarseClock reads its base directly,
// so a component that outlives it never sees time stand still. The zero
// value is NOT usable — construct with [NewCoarse].
type CoarseClock struct {
	// base is the clock read every resolution, and after Stop on every call.
	base Timed
	// reading is the last published instant; nil once stopped, when Now reads
	// base instead. One pointer per tick: the reader's whole cost is a load.
	reading atomic.Pointer[time.Time]
	ticker  Ticker
	quit    chan struct{}
	// running is the publishing goroutine, which Stop waits out.
	running sync.WaitGroup
	once    sync.Once
}

// NewCoarse returns a CoarseClock that publishes base's reading every
// resolution, on a ticker of base: a ManualClock base makes it
// deterministic. A nil base is [System]. A resolution that is not positive
// panics, as base's NewTicker does. The caller owns the goroutine it starts
// and ends it with Stop.
func NewCoarse(base Timed, resolution time.Duration) *CoarseClock {
	//: refused here, at the call that made the mistake.
	requirePositivePeriod("NewCoarse", resolution)
	//: nil is the production default, as everywhere a clock is injected.
	if base == nil {
		base = System
	}
	built := &CoarseClock{
		base:   base,
		ticker: base.NewTicker(resolution),
		quit:   make(chan struct{}),
	}
	built.store()
	built.running.Go(built.publish)
	//: running; the first reading is the base's at construction.
	return built
}

// publish is the goroutine: every tick it stores base's reading, until Stop.
func (c *CoarseClock) publish() {
	//: one store per tick, nothing else.
	for {
		select {
		//: Stop asked for the end.
		case <-c.quit:
			//: done publishing.
			return
		//: a tick: publish the base's current reading.
		case _, ticking := <-c.ticker.C():
			//: a ticker's channel is never closed; were one, nothing more could come.
			if !ticking {
				//: done publishing.
				return
			}
			c.store()
		}
	}
}

// store publishes the base's current reading — monotonic part included, so a
// Since across two readings measures what base would.
func (c *CoarseClock) store() {
	c.reading.Store(new(c.base.Now()))
}

// Now returns the last published reading, at most one resolution behind the
// base; after Stop, the base's own reading.
func (c *CoarseClock) Now() time.Time {
	published := c.reading.Load()
	//: a stopped clock no longer publishes: read the base rather than freeze.
	if published == nil {
		//: exact, and as costly as the base.
		return c.base.Now()
	}
	//: one atomic load and a copy.
	return *published
}

// Since returns the time elapsed since t by this clock's reading.
func (c *CoarseClock) Since(t time.Time) time.Duration {
	//: the same coarse reading Now gives.
	return c.Now().Sub(t)
}

// Stop ends the publishing goroutine and returns once it has exited. It is
// idempotent; after it, Now reads the base directly.
func (c *CoarseClock) Stop() {
	//: one shutdown, however many callers.
	c.once.Do(func() {
		c.ticker.Stop()
		close(c.quit)
		c.running.Wait()
		c.reading.Store(nil)
	})
}
