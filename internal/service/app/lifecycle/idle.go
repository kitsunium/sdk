package lifecycle

import (
	"context"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// minIdleEvery is the shortest interval IdleStop asks its probe at.
const minIdleEvery time.Duration = time.Millisecond

// idleEveryShare is the share of the grace IdleStop checks at when
// IdleConfig.Every is not positive.
const idleEveryShare time.Duration = 10

// IdleConfig tunes [IdleStop]: what idle means, for how long, how often it is
// asked and on which clock.
type IdleConfig struct {
	// Idle reports whether the application is idle now. It is called on the
	// watcher's goroutine, one call at a time, and must be short. Required.
	Idle func() bool
	// Grace is how long Idle must keep answering true before the stop.
	// Required, positive.
	Grace time.Duration
	// Every is how often Idle is asked. Not positive means a tenth of Grace,
	// and never less than a millisecond; the stop comes at most Every after
	// the grace has elapsed.
	Every time.Duration
	// Clock is the time source the checks run on. Nil means clock.System; a
	// clock.ManualClock drives a test without a sleep.
	Clock clock.Timed
}

// IdleStop returns a context derived from ctx that is also cancelled, with
// IdleReached as its cause, once cfg.Idle has answered true at every check
// for cfg.Grace; a false answer starts the count again. The first check is
// made at once, so an application idle from its start stops one grace later.
// The CancelFunc must be called. A nil Idle or a Grace that is not positive
// is refused with IdleMisconfigured.
//
// It is how a process that serves while it has clients — a per-user daemon,
// a broker for the sessions of one machine — ends on its own when the last
// one leaves: give the context to Run.
//
//	ctx, stop, err := lifecycle.IdleStop(ctx, lifecycle.IdleConfig{
//		Idle:  func() bool { return clients.Count() == 0 },
//		Grace: 30 * time.Second,
//	})
//	defer stop()
//	err = lifecycle.Run(ctx, app, lifecycle.RunConfig{})
//
// Goroutine lifecycle: one per call, ended when the idle grace elapses or the
// returned context ends, whichever is first.
func IdleStop(ctx context.Context, cfg IdleConfig) (context.Context, context.CancelFunc, error) {
	//: a stop with nothing to ask, or no span to wait, could never decide.
	if cfg.Idle == nil {
		//: refused, naming the field.
		return nil, nil, idleMisconfigured("Idle")
	}
	//: a grace of zero or less would stop at once, or never.
	if cfg.Grace <= 0 {
		//: refused, naming the field.
		return nil, nil, idleMisconfigured("Grace")
	}
	//: the defaults the zero values stand for.
	if cfg.Clock == nil {
		cfg.Clock = clock.System
	}
	//: a tenth of the grace, not below the floor.
	if cfg.Every <= 0 {
		cfg.Every = max(cfg.Grace/idleEveryShare, minIdleEvery)
	}
	derived, cancel := context.WithCancelCause(ctx)
	ticker := cfg.Clock.NewTicker(cfg.Every)
	go watchIdle(derived, cancel, ticker, cfg)
	//: the caller's cancel ends the watcher too.
	return derived, func() { cancel(context.Canceled) }, nil
}

// watchIdle asks cfg.Idle at once and on every tick, and cancels with
// IdleReached once it has answered true for the whole grace.
func watchIdle(ctx context.Context, cancel context.CancelCauseFunc, ticker clock.Ticker, cfg IdleConfig) {
	defer ticker.Stop()
	var since time.Time
	//: one check now, then one per tick, until the context ends.
	for {
		//: an answer of busy starts the count again.
		if !cfg.Idle() {
			since = time.Time{}
		} else {
			now := cfg.Clock.Now()
			//: the first idle answer starts the count.
			if since.IsZero() {
				since = now
			}
			//: idle for the whole grace: the stop.
			if now.Sub(since) >= cfg.Grace {
				cancel(corelc.IdleReached)
				//: done.
				return
			}
		}
		select {
		case <-ctx.Done():
			//: the parent or the caller ended it first.
			return
		case _, ok := <-ticker.C():
			//: a ticker that closed its channel ticks no more.
			if !ok {
				//: nothing left to wait on.
				return
			}
		}
	}
}

// idleMisconfigured names the field IdleStop refused.
func idleMisconfigured(field string) error {
	//: once, at construction.
	return kerrs.Wrap(corelc.IdleMisconfigured, kerrs.WrapParams{}, kerrs.String("field", field))
}
