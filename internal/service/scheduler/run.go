// Package scheduler — hosts the run loop: waiting, firing, and draining.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Run drives the registered entries until ctx is cancelled.
//
// It waits for every in-flight job before returning — a job still running
// after Run has returned would leave the caller believing the scheduler
// stopped while work continued. A job that ignores ctx therefore delays
// shutdown by its own duration; that is the documented cost of not abandoning
// it.
func (s *scheduler) Run(ctx context.Context) error {
	entries, err := s.begin()
	//: a second concurrent Run is refused, not silently joined.
	if err != nil {
		//: propagate SCHEDULER_RUNNING unchanged.
		return err
	}
	//: released last, so a caller may Add and Run again afterwards.
	defer s.finish()
	var wg sync.WaitGroup
	//: the graceful drain. Deferred so it also covers the early returns below.
	defer wg.Wait()
	now := s.clk.Now()
	//: every entry is armed from ONE reading, so two entries with the same
	//: schedule get the same first deadline.
	for _, e := range entries {
		e.arm(now)
	}
	//: the loop owns the timer and the firing from here.
	return s.loop(ctx, entries, &wg)
}

// loop waits for the earliest deadline, fires what is due, and re-arms.
func (s *scheduler) loop(ctx context.Context, entries []*entry, wg *sync.WaitGroup) error {
	due, ok := earliest(entries)
	//: no entry will ever fire — including the legitimate case of a scheduler
	//: with no entries at all, which waits for ctx exactly like a busy one.
	if !ok {
		//: an idle scheduler is not an error; ADR 0031's trap is an inert
		//: POLICY, not an empty one.
		return s.idle(ctx)
	}
	//: one timer, reset each pass, so the armed-wait count a test blocks on
	//: returns to exactly one between fires.
	timer := s.clk.NewTimer(due.Sub(s.clk.Now()))
	//: release it on every exit path.
	defer timer.Stop()
	//: bind the delivery channel once. It belongs to the timer THIS function
	//: created and is never closed (clock.Timer's contract), so the comma-ok
	//: form KTN-GOROUTINE-CHANRECV-OK asks for would add a branch no test can
	//: reach rather than a guard.
	fires := timer.C()
	//: wait, fire, re-arm, until cancellation.
	for {
		//: race the earliest deadline against cancellation.
		select {
		//: cancellation is a clean stop; the drain happens in Run.
		case <-ctx.Done():
			//: Run's deferred wg.Wait does the draining.
			return nil
		//: read the clock AFTER waking, never the delivered instant: on a
		//: machine that slept the wake is late, and HOW late is exactly what
		//: the missed-deadline rule needs to see.
		case <-fires:
			s.fire(ctx, entries, wg, s.clk.Now())
		}
		due, ok = earliest(entries)
		//: every entry became exhausted while we were firing.
		if !ok {
			//: fall back to waiting for cancellation.
			return s.idle(ctx)
		}
		//: a deadline already in the past resets to a non-positive duration,
		//: which both clocks deliver at once — so a backlog drains without a
		//: special case.
		timer.Reset(due.Sub(s.clk.Now()))
	}
}

// idle waits for cancellation with nothing left to fire.
func (s *scheduler) idle(ctx context.Context) error {
	<-ctx.Done()
	//: a scheduler that ran nothing still stopped cleanly.
	return nil
}

// earliest returns the soonest armed deadline across entries.
func earliest(entries []*entry) (due time.Time, found bool) {
	//: a linear scan: an SDK scheduler holds tens of entries, not a heap's
	//: worth, and a heap would need re-ordering on every fire anyway.
	for _, e := range entries {
		//: exhausted entries hold no deadline.
		if !e.armed {
			//: skip.
			continue
		}
		//: keep the soonest.
		if !found || e.next.Before(due) {
			due, found = e.next, true
		}
	}
	//: false means nothing is armed.
	return due, found
}

// fire starts every entry that is due at now, or reports it skipped.
//
// Lifecycle: one goroutine per fired entry, registered on wg before it starts
// and released when the job returns. Run's deferred wg.Wait is the single join
// point, so nothing started here outlives Run — a job that ignores ctx delays
// the return rather than escaping it.
func (s *scheduler) fire(ctx context.Context, entries []*entry, wg *sync.WaitGroup, now time.Time) {
	//: registration order, so two entries due at the same instant start in an
	//: order the caller can predict.
	for _, e := range entries {
		scheduled, missed, ok := e.due(now)
		//: not due on this pass.
		if !ok {
			//: next entry.
			continue
		}
		//: the safe default: a previous run of THIS entry is still going, so
		//: this fire is dropped rather than run beside it. Queueing would grow
		//: without bound behind a persistently slow job; running in parallel
		//: would duplicate a side effect the SDK cannot know is safe.
		if !e.allowOverlap && e.busy.Load() {
			//: report the skip — an unreported skip is indistinguishable from
			//: an entry that is keeping up.
			s.emit(coresched.ResultValue{
				Name: e.name, Scheduled: scheduled, Missed: missed, Skipped: true,
			})
			//: next entry.
			continue
		}
		e.busy.Store(true)
		wg.Add(1)
		//: each job runs on its own goroutine so a slow one never delays
		//: another entry's deadline — which is what makes overlap a question
		//: worth answering in the first place.
		go s.run(ctx, wg, e, scheduled, missed)
	}
}

// run executes one job and publishes its result.
func (s *scheduler) run(ctx context.Context, wg *sync.WaitGroup, e *entry, scheduled time.Time, missed int) {
	defer wg.Done()
	started := s.clk.Now()
	jobErr := invoke(ctx, e)
	result := coresched.ResultValue{
		Name:      e.name,
		Scheduled: scheduled,
		Started:   started,
		Finished:  s.clk.Now(),
		Missed:    missed,
		Err:       jobErr,
	}
	//: free the slot BEFORE publishing, so an observer that has seen the
	//: result can rely on the next fire not being skipped for overlap.
	e.busy.Store(false)
	s.emit(result)
}

// invoke runs the job, converting a panic into a typed error.
func invoke(ctx context.Context, e *entry) (err error) {
	//: a panicking job must not reach the runtime: one job's bug would take
	//: down the process and every other entry with it.
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: leave err as the job returned it.
			return
		}
		//: the recovered value travels as a FIELD, never as the wrap origin,
		//: so a panic carrying an *errs.Error cannot hijack JOB_PANICKED.
		err = kerrs.Wrap(coresched.JobPanicked, kerrs.WrapParams{},
			kerrs.String("job", e.name), kerrs.String("panic", fmt.Sprint(value)))
	}()
	//: the job gets Run's own context, so cancelling it reaches in here.
	return e.job(ctx)
}
