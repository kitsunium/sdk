// Package statemachine — hosts the machine's own loop: Step, one pass over
// what is due; Run, which paces the passes and sleeps until the next
// transition due or a write, never polling; and what OnLoop is told.
package statemachine

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// WakeStart is the first run, when Run starts.
	WakeStart Wake = iota + 1
	// WakeDue is a run the loop woke for by itself: a transition fell due.
	WakeDue
	// WakeChange is a run a write woke the loop for.
	WakeChange
)

const (
	// LoopRunStarted reports a run starting: Wake and Started are set.
	LoopRunStarted LoopEventKind = iota + 1
	// LoopRunEnded reports a run ending: every field is set.
	LoopRunEnded
	// LoopWaiting reports the loop re-armed to wake earlier, at Next, because
	// a write arrived while it slept.
	LoopWaiting
)

// What ended a sleep of the loop.
const (
	// sleptCanceled is the caller's context ending.
	sleptCanceled slept = iota
	// sleptDue is the target instant coming.
	sleptDue
	// sleptWoken is a write's wake.
	sleptWoken
)

// Wake says why a run of the loop started.
type Wake uint8

// LoopEventKind says what a [LoopEvent] reports.
type LoopEventKind uint8

// slept says what ended a sleep of the loop.
type slept uint8

// String returns "start", "due" or "change"; "" outside the set.
func (w Wake) String() string {
	//: one name per reason.
	switch w {
	//: the first run.
	case WakeStart:
		//: named.
		return "start"
	//: the loop's own timer.
	case WakeDue:
		//: named.
		return "due"
	//: a write.
	case WakeChange:
		//: named.
		return "change"
	}
	//: outside the set.
	return ""
}

// String returns "run-started", "run-ended" or "waiting"; "" outside the set.
func (k LoopEventKind) String() string {
	//: one name per kind.
	switch k {
	//: a run starts.
	case LoopRunStarted:
		//: named.
		return "run-started"
	//: a run ends.
	case LoopRunEnded:
		//: named.
		return "run-ended"
	//: the loop re-armed.
	case LoopWaiting:
		//: named.
		return "waiting"
	}
	//: outside the set.
	return ""
}

// LoopEvent is what Config.OnLoop is told about [StateMachine.Run]. Which fields
// are set depends on Kind.
type LoopEvent struct {
	// Err joins what failed in the run; nil when nothing did.
	Err error
	// Started is when the run started.
	Started time.Time
	// Ended is when the run ended.
	Ended time.Time
	// Next is when the loop wakes by itself next — never sooner than
	// Config.MinGap after the run ended; zero when only a write will wake it.
	Next time.Time
	// Fired counts the transitions the run stored.
	Fired int
	// Failed counts the transitions the run could not fire.
	Failed int
	// Kind says what is reported.
	Kind LoopEventKind
	// Wake says why the run started.
	Wake Wake
}

// stepResult is what one pass did.
type stepResult struct {
	next   time.Time
	err    error
	fired  int
	failed int
}

// Run is the machine's own loop: it fires the timer and guard transitions as
// they fall due, until ctx ends, and returns nil then. It returns
// [LoopRunning] at once when another Run or a Step of this machine is going.
//
// It never polls. A run fires what is due — per entity, the first declared
// transition due, one per entity per run — and the loop then sleeps until the
// next transition due or until a write wakes it: [StateMachine.Changed], or a
// transition stored by Start or Fire. No run starts sooner than
// Config.MinGap after the previous one ended, so a burst of writes is one
// run. A machine with no timer and no guard never runs, and Run just waits for
// ctx.
//
// An entity whose transition failed is tried again after Config.Backoff,
// counted per entity: one failing entity delays nobody else. Everything runs
// on the calling goroutine, so a caller's pprof labels cover every hook the
// loop runs.
func (m *StateMachine[E, S]) Run(ctx context.Context) error {
	//: one loop per machine.
	if !m.running.CompareAndSwap(false, true) {
		//: the other one keeps going.
		return LoopRunning
	}
	defer m.running.Store(false)
	//: nothing moves an entity by itself: there is no run to make.
	if len(m.plan.auto) == 0 {
		<-ctx.Done()
		//: a clean stop, like a loop that had something to do.
		return nil
	}
	wake := WakeStart
	//: a run, then a wait, until the context ends.
	for {
		at, floor := m.run(ctx, wake)
		//: the caller stopped the loop while it ran.
		if ctx.Err() != nil {
			//: a clean stop.
			return nil
		}
		var ok bool
		//: the wait ends with the next run's reason, or with the context.
		if wake, ok = m.wait(ctx, at, floor); !ok {
			//: a clean stop.
			return nil
		}
	}
}

// Step makes one pass over what is due now: every entity written since the
// last pass is evaluated, and every entity whose transition is due fires it
// — the first declared one due, one transition per entity. It returns when
// the loop should look again by itself: the current instant when entities
// wait to be evaluated, the earliest due instant otherwise, the zero time when
// nothing is scheduled; and the failures joined.
//
// It is for a caller that drives the machine itself instead of Run — a test,
// another scheduler. It returns [LoopRunning] when Run or another Step is
// going.
func (m *StateMachine[E, S]) Step(ctx context.Context) (time.Time, error) {
	//: one pass at a time, and never beside Run.
	if !m.running.CompareAndSwap(false, true) {
		//: nothing was done.
		return time.Time{}, LoopRunning
	}
	defer m.running.Store(false)
	out := m.step(ctx)
	//: when to look again, and what failed.
	return out.next, out.err
}

// run is one run of the loop, told to OnLoop. It returns when the loop wakes
// by itself next and the earliest the next run may start.
func (m *StateMachine[E, S]) run(ctx context.Context, wake Wake) (at, floor time.Time) {
	started := m.cfg.clock.Now()
	m.cfg.emit(&LoopEvent{Kind: LoopRunStarted, Wake: wake, Started: started})
	out := m.step(ctx)
	ended := m.cfg.clock.Now()
	floor = ended.Add(m.cfg.minGap)
	at = out.next
	//: never sooner than the pace allows, even for what is already due.
	if !at.IsZero() && at.Before(floor) {
		at = floor
	}
	m.cfg.emit(&LoopEvent{
		Kind: LoopRunEnded, Wake: wake, Started: started, Ended: ended, Next: at,
		Fired: out.fired, Failed: out.failed, Err: out.err,
	})
	//: the wait's target and its floor.
	return at, floor
}

// wait sleeps until at, or until a write — but then not before floor — and
// says why the next run starts. It reports false when ctx ends.
func (m *StateMachine[E, S]) wait(ctx context.Context, at, floor time.Time) (Wake, bool) {
	wake := WakeDue
	//: a write before the floor moves the target, and the wait goes on.
	for {
		outcome := m.sleep(ctx, at)
		//: the caller stops the loop.
		if outcome == sleptCanceled {
			//: no next run.
			return 0, false
		}
		//: the target came.
		if outcome == sleptDue {
			//: why the target was set.
			return wake, true
		}
		//: a token left by a write the last run already served: the dirty set
		//: is the truth, and the loop is its only consumer.
		if !m.agenda.pending() {
			//: keep waiting for the same target.
			continue
		}
		//: past the floor already: run now.
		if !m.cfg.clock.Now().Before(floor) {
			//: a write woke it.
			return WakeChange, true
		}
		at, wake = m.retarget(at, floor, wake)
	}
}

// retarget moves the wait's target to floor when a write arrived before it
// and floor comes sooner than the current target, and tells OnLoop.
func (m *StateMachine[E, S]) retarget(at, floor time.Time, wake Wake) (time.Time, Wake) {
	//: the current target is later than the floor, or there is none.
	if at.IsZero() || floor.Before(at) {
		m.cfg.emit(&LoopEvent{Kind: LoopWaiting, Next: floor})
		//: the floor, for the write.
		return floor, WakeChange
	}
	//: the current target already comes first.
	return at, wake
}

// sleep blocks until at comes — never, when at is zero — until a write wakes
// the loop, or until ctx ends, and says which.
func (m *StateMachine[E, S]) sleep(ctx context.Context, at time.Time) slept {
	timer, fire := m.arm(at)
	defer stopTimer(timer)
	//: the three ways out of a sleep.
	select {
	//: the caller stops the loop.
	case <-ctx.Done():
		//: canceled.
		return sleptCanceled
	//: the target came.
	case <-fire:
		//: due.
		return sleptDue
	//: a write arrived.
	case <-m.agenda.wake:
		//: woken.
		return sleptWoken
	}
}

// arm returns a timer for at and its channel; no timer and a nil channel —
// which never delivers — when at is zero.
func (m *StateMachine[E, S]) arm(at time.Time) (clock.Timer, <-chan time.Time) {
	//: nothing scheduled: only a write or the context end the wait.
	if at.IsZero() {
		//: a nil channel blocks its select case forever.
		return nil, nil
	}
	timer := m.cfg.clock.NewTimer(at.Sub(m.cfg.clock.Now()))
	//: a past target fires at once, on both clocks.
	return timer, timer.C()
}

// stopTimer stops timer when there is one.
func stopTimer(timer clock.Timer) {
	//: a wait with no target has no timer.
	if timer != nil {
		timer.Stop()
	}
}

// step is one pass: take what is due or written, and look at each entity once.
// Every failure is reported here, once the entity's lock is released.
func (m *StateMachine[E, S]) step(ctx context.Context) stepResult {
	now := m.cfg.clock.Now()
	keys := m.agenda.take(now)
	var out stepResult
	var failures []error
	//: each entity once, in the agenda's order.
	for i, key := range keys {
		//: stopping: what was not reached waits for the next pass.
		if ctx.Err() != nil {
			m.agenda.requeue(keys[i:])
			//: the rest is left untouched.
			break
		}
		looked := m.guarded(ctx, key, now)
		m.cfg.reportErr(ctx, looked.failed)
		m.cfg.reportErr(ctx, looked.late)
		//: tally the outcome.
		switch {
		//: counted and joined for the run.
		case looked.failed != nil:
			out.failed++
			failures = append(failures, looked.failed)
		//: one transition stored.
		case looked.fired:
			out.fired++
		}
	}
	out.err = errors.Join(failures...)
	out.next = m.agenda.next(m.cfg.clock.Now())
	//: what the pass did, and when to look again.
	return out
}

// pass is what looking at one entity did: whether a transition was stored,
// the failure that holds the entity back, and a failure that is not the
// entity's — a journal write — which is only reported.
type pass struct {
	failed error
	late   error
	fired  bool
}

// guarded is process with a panic of the caller's own code — a store method,
// a journal, an observer's start — recovered as that entity's failure: the
// loop goes on, reports it, and tries the entity again after its backoff. The
// entity's lock and flights are released by process's own deferred calls on
// the way out. An observer's end has its panic recovered apart, by finish.
func (m *StateMachine[E, S]) guarded(ctx context.Context, key string, now time.Time) (looked pass) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: the outcome stands as process returned it.
			return
		}
		//: the value travels as a field, never as the wrap origin.
		looked = pass{failed: m.failure(key, now, errs.Wrap(LoopPanicked, errs.WrapParams{}, errs.String("key", key),
			errs.String("panic", fmt.Sprint(value)), errs.String("stack", string(debug.Stack()))))}
	}()
	//: the entity's pass, unguarded.
	return m.process(ctx, key, now)
}

// process looks at one entity: it reads it, asks its automatic transitions
// what is due, and fires the first declared one due — or schedules the
// earliest.
func (m *StateMachine[E, S]) process(ctx context.Context, key string, now time.Time) pass {
	release, err := m.locks.acquire(ctx, key)
	//: stopping while a caller's transition held the entity.
	if err != nil {
		m.agenda.requeue([]string{key})
		//: not a failure: the next pass looks again.
		return pass{}
	}
	defer release()
	looked, due, ok := m.look(ctx, key, now)
	//: nothing to fire: the look is the whole pass.
	if !ok {
		//: read, recorded, scheduled.
		return looked
	}
	fired := m.fire(ctx, due, release, now)
	fired.late = errors.Join(looked.late, fired.late)
	//: the first declared transition due, fired.
	return fired
}

// look reads key under a read's flight — so a deletion that arrives before
// the record is made wins — records what it read, and returns the transition
// due, when there is one. An entity deleted meanwhile fires nothing and
// leaves the agenda. The caller holds the entity's lock.
func (m *StateMachine[E, S]) look(ctx context.Context, key string, now time.Time) (looked pass, due pending[E, S], ok bool) {
	f := m.book.read(key)
	defer func() {
		//: deleted while it was read: nothing fires, nothing stays planned.
		if _, deleted := m.book.land(key, f); deleted {
			m.agenda.forget(key)
			looked, ok = pass{late: looked.late}, false
		}
	}()
	//: the read, the record and the verdict.
	return m.inspect(ctx, key, now, f)
}

// inspect is the body of look: it reads the entity, reconciles its record,
// and returns the first declared transition due — or schedules the earliest.
func (m *StateMachine[E, S]) inspect(ctx context.Context, key string, now time.Time, f *flight) (pass, pending[E, S], bool) {
	var none pending[E, S]
	entity, found, err := m.store.Get(ctx, key)
	//: the store could not say: retried after the backoff.
	if err != nil {
		//: counted as a failure of this entity.
		return pass{failed: m.failure(key, now, storeFailure(err, "get"))}, none, false
	}
	//: gone without a notification: forgotten, not failed.
	if !found {
		//: the journal's verdict on forgetting it, if any.
		return pass{late: m.Deleted(ctx, key)}, none, false
	}
	state := *m.plan.state(&entity)
	changed, late := m.book.reconcile(ctx, key, state, m.cfg.clock.Now(), f)
	//: a state entered behind the machine's back starts afresh.
	if changed {
		m.agenda.restart(key)
	}
	entered, _ := m.book.entered(key)
	v, err := m.plan.evaluate(entity, state, entered, now)
	//: a guard or an instant function panicked: retried after its backoff.
	if err != nil {
		//: like any failed transition.
		return pass{failed: m.failure(key, now, err), late: late}, none, false
	}
	//: nothing due now, or held back by an earlier failure.
	if v.fire == nil || m.agenda.heldBack(key, now) {
		m.reschedule(key, v)
		//: nothing to fire.
		return pass{late: late}, none, false
	}
	//: the first declared transition due.
	return pass{late: late}, pending[E, S]{entity: entity, key: key, arrow: *v.fire}, true
}

// fire runs one transition the loop decided on, bracketed by Config.Observe.
// The entity's lock is held and its release handed over; the transition
// reports its own late failures once it has released it. The observer hears
// the outcome only once the agenda holds it, so a panic of the observer's own
// is reported and changes nothing: a stored transition is never retried.
func (m *StateMachine[E, S]) fire(ctx context.Context, p pending[E, S], release func(), now time.Time) pass {
	x := p.arrow
	tctx, done := m.cfg.bracket(ctx, &FiringValue[S]{Key: p.key, Event: x.event, From: x.from, To: x.to, Trigger: x.trigger})
	landed, err := m.transition(tctx, p, release)
	fired := m.outcome(ctx, p.key, now, landed.gone, err)
	fired.late = errors.Join(fired.late, m.cfg.finish(done, p.key, err))
	//: what the agenda recorded, and what is only reported.
	return fired
}

// outcome records on the agenda what a transition the loop fired came to.
// gone says the store refused the write because the entity was deleted.
func (m *StateMachine[E, S]) outcome(ctx context.Context, key string, now time.Time, gone bool, err error) pass {
	//: the outcome decides what the agenda remembers.
	switch {
	//: stored: whatever failed before is behind it.
	case err == nil:
		m.agenda.succeeded(key)
		//: one transition fired.
		return pass{fired: true}
	//: deleted while it ran, without a word to the machine: forgotten
	//: everywhere, as a read that finds nothing — and nothing failed.
	case gone:
		//: the journal's verdict on forgetting it, if any.
		return pass{late: m.Deleted(ctx, key)}
	}
	//: a hook or the store refused it: retried after the backoff.
	return pass{failed: m.failure(key, now, err)}
}

// failure counts a failure of key and schedules its retry; the step reports
// it once the entity's lock is released.
func (m *StateMachine[E, S]) failure(key string, now time.Time, err error) error {
	m.agenda.failed(key, now, m.cfg.backoff)
	//: joined into the run's error, and reported.
	return err
}

// reschedule puts key back on the agenda at its next due instant, or takes it
// off when nothing will fall due by itself.
func (m *StateMachine[E, S]) reschedule(key string, v verdict[E, S]) {
	//: something is due now but held back: its backoff decides.
	if v.fire != nil {
		m.agenda.schedule(key, time.Time{})
		//: schedule clamps to the backoff.
		return
	}
	//: nothing will fall due by itself: only a write can change that.
	if v.next.IsZero() {
		m.agenda.unschedule(key)
		//: off the agenda.
		return
	}
	m.agenda.schedule(key, v.next)
}
