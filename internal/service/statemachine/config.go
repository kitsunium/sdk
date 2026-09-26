// Package statemachine — hosts Config, a machine's construction parameters,
// and the defaults its zero fields resolve to.
package statemachine

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/resilience"
)

// DefaultMinGap is the least time between the end of one run of a machine's
// loop and the start of the next, when Config.MinGap is not positive: a burst
// of writes is one run, and a transition chain advances one step per run.
const DefaultMinGap time.Duration = time.Second

// DefaultMaxHistory is how many transitions a record keeps when
// Config.MaxHistory is not positive.
const DefaultMaxHistory int = 20

// defaultBackoffBase and defaultBackoffMax bound the wait before an entity
// whose automatic transition failed is tried again, when Config.Backoff has no
// BaseDelay: 1s, doubling, held at a minute.
const (
	defaultBackoffBase time.Duration = time.Second
	defaultBackoffMax  time.Duration = time.Minute
)

// FiringValue is a transition the machine's own loop is about to fire, as
// Config.Observe is told it.
type FiringValue[S comparable] struct {
	// Key is the entity's key.
	Key string
	// Event is the transition's name.
	Event string
	// From is the state the entity is in.
	From S
	// To is the state it is about to enter.
	To S
	// Trigger is a delay, a deadline or a guard: the loop fires nothing else.
	Trigger corestm.Trigger
}

// Config parameterises NewStateMachine. Only Store is required; every other zero
// field resolves to a working default.
type Config[E any, S comparable] struct {
	// Store holds the entities. Required.
	Store corestm.Store[E]
	// Journal keeps each entity's record beside the store. Nil keeps the
	// records in memory: after a restart every entity re-enters its state
	// when the machine opens, which restarts every After timer.
	Journal corestm.Journal[S]
	// Clock is what the machine stamps and waits on. Nil is clock.System; a
	// clock.ManualClock drives every timer of a test.
	Clock clock.Timed
	// Actor reads, from the context a caller hands Start or Fire, who fired
	// the transition; the result lands in the record's history and in the
	// change hooks receive. Never called for a transition the loop fires.
	// Nil records nobody.
	Actor func(ctx context.Context) string
	// Observe brackets every transition the machine's OWN loop fires — the
	// ones no caller can wrap: it is called before the transition with the
	// loop's context, returns the context the transition's hooks and store
	// calls run under — a tracer puts its span there — and a function the
	// engine calls with the outcome once the OnTransition hooks have run and
	// the loop has recorded it: a panic there is reported as LoopPanicked and
	// changes nothing, the transition stands. A caller wraps its own Start and
	// Fire itself. It is called while the loop holds the entity, so it must
	// not fire that entity. Nil observes nothing.
	Observe func(ctx context.Context, firing FiringValue[S]) (context.Context, func(err error))
	// Report receives every error no caller's return value carries: an
	// OnTransition hook's failure, a Journal write that failed, and each
	// transition the loop could not fire. It is called with no lock of the
	// machine held, so it may read the machine — its census, a record — or
	// fire it. Nil drops them — the SDK does not write to stderr on the
	// caller's behalf (ADR 0030) — so wire it.
	Report func(ctx context.Context, err error)
	// OnLoop is told what Run does: a run starting, a run ending with what
	// it fired and when the loop wakes next, and the loop re-arming earlier
	// because a write arrived. Called on the loop's goroutine; keep it short.
	// Nil observes nothing.
	OnLoop func(event LoopEvent)
	// MinGap is the least time between the end of one run of the loop and
	// the start of the next. Not positive means DefaultMinGap.
	MinGap time.Duration
	// Backoff is how long an entity whose automatic transition failed waits
	// before it is tried again, by consecutive failures. A zero BaseDelay
	// means 1s doubling to a minute.
	Backoff resilience.BackoffValue
	// MaxHistory is how many transitions each record keeps. Not positive
	// means DefaultMaxHistory.
	MaxHistory int
}

// settings is a Config with every default resolved.
type settings[E any, S comparable] struct {
	clock   clock.Timed
	actor   func(context.Context) string
	observe func(context.Context, FiringValue[S]) (context.Context, func(error))
	report  func(context.Context, error)
	onLoop  func(LoopEvent)
	backoff resilience.BackoffValue
	minGap  time.Duration
	history int
}

// resolve returns cfg's behaviour with every default applied.
func resolve[E any, S comparable](cfg *Config[E, S]) settings[E, S] {
	s := settings[E, S]{
		clock: cfg.Clock, actor: cfg.Actor, observe: cfg.Observe, report: cfg.Report, onLoop: cfg.OnLoop,
		backoff: cfg.Backoff, minGap: cfg.MinGap, history: cfg.MaxHistory,
	}
	//: the wall clock when the caller does not bring one.
	if s.clock == nil {
		s.clock = clock.System
	}
	//: a loop that could run back to back would spin on a chain of guards.
	if s.minGap <= 0 {
		s.minGap = DefaultMinGap
	}
	//: an immediate retry of a failing transition hammers what failed.
	if s.backoff.BaseDelay <= 0 {
		s.backoff = resilience.BackoffValue{BaseDelay: defaultBackoffBase, MaxDelay: defaultBackoffMax}
	}
	//: a record with no history would say nothing a reader could use.
	if s.history <= 0 {
		s.history = DefaultMaxHistory
	}
	//: the resolved behaviour, every field set.
	return s
}

// sign records on p who fired it, as Config.Actor reads it from the caller's
// context; nobody when there is no Actor.
func (s *settings[E, S]) sign(ctx context.Context, p *pending[E, S]) {
	//: the caller's own convention, when it has one.
	if s.actor != nil {
		p.actor = s.actor(ctx)
	}
}

// bracket starts the observation of a transition the loop fires.
func (s *settings[E, S]) bracket(ctx context.Context, firing *FiringValue[S]) (context.Context, func(error)) {
	//: nobody observes: the loop's context, and an outcome nobody reads.
	if s.observe == nil {
		//: a no-op end.
		return ctx, func(error) {}
	}
	tctx, done := s.observe(ctx, *firing)
	//: an observer that returns no end still gets a callable one.
	if done == nil {
		done = func(error) {}
	}
	//: the observer's context may be nil only by mistake; keep the loop's.
	if tctx == nil {
		tctx = ctx
	}
	//: the transition runs under whatever the observer returned.
	return tctx, done
}

// finish hands the observer's end the outcome of the transition it
// bracketed, with a panic of its own recovered and returned: the transition
// is stored or refused already, and the panic only reported.
func (s *settings[E, S]) finish(done func(error), key string, err error) (failed error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: nothing to report.
			return
		}
		//: the value travels as a field, never as the wrap origin.
		failed = errs.Wrap(LoopPanicked, errs.WrapParams{}, errs.String("key", key), errs.String("call", "observe-end"),
			errs.String("panic", fmt.Sprint(value)), errs.String("stack", string(debug.Stack())))
	}()
	done(err)
	//: the observer heard it.
	return nil
}

// reportErr hands err to Config.Report, when there is one.
func (s *settings[E, S]) reportErr(ctx context.Context, err error) {
	//: a nil hook drops it, by the documented contract.
	if s.report != nil && err != nil {
		s.report(ctx, err)
	}
}

// emit hands event to Config.OnLoop, when there is one.
func (s *settings[E, S]) emit(event *LoopEvent) {
	//: a nil hook observes nothing.
	if s.onLoop != nil {
		s.onLoop(*event)
	}
}
