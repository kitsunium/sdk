// Package kit — the daemon's loops: their states, the scheduler that fires
// the jobs, and the goroutine labels that name them.
package kit

import (
	"context"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// errOverlapped is what a scheduled job returns instead of running when a
// run the Studio started is still going: the fire is skipped, like one that
// overlaps a scheduled run.
var errOverlapped = errs.New(CodeJobOverlapped, "JOB_OVERLAPPED", "a run of this job is already going", "kit: the scheduler skipped a run whose previous run had not ended")

// schedRunner runs the app's one SDK scheduler, which fires the jobs, and
// counts its turns on the scheduler's own loop.
type schedRunner struct {
	a     *App
	sched scheduler.Scheduler
	loop  *loopState

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
}

// provenanceOf says who wrote the code that loops, by kind.
func provenanceOf(kind string) (provenance, library string) {
	switch kind {
	case model.LoopJob, model.LoopScheduler:
		return model.ProvenanceLibrary, "sdk/v1/app/scheduler"
	case model.LoopConsumer:
		return model.ProvenanceLibrary, "sdk/v1/data/queue"
	case model.LoopHTTP:
		return model.ProvenanceLibrary, "sdk/v1/net/server"
	case model.LoopTimer:
		return model.ProvenanceLibrary, "sdk/v1/app/statemachine"
	case model.LoopWake:
		return model.ProvenanceKit, "kit"
	case model.LoopRoutine:
		return model.ProvenanceProduct, ""
	case model.LoopRotation:
		return model.ProvenanceLibrary, "sdk/v1/security/secret"
	default:
		// A kind kit does not know has no provenance.
	}
	return "", ""
}

// begin marks a run of the loop as started, for wake — interval, topic,
// deadline, manual, start, or "" when the caller does not say — and streams
// the loop's new state.
func (l *loopState) begin(a *App, wake string) {
	a.mu.Lock()
	snapshot := l.beginLocked(wake)
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}

// tryBegin is begin, unless a run is already going: the scheduler's own
// runs never overlap, and a run the Studio started must not be overlapped
// either. The check and the claim are one step.
func (l *loopState) tryBegin(a *App, wake string) bool {
	a.mu.Lock()
	if l.active > 0 {
		a.mu.Unlock()
		return false
	}
	snapshot := l.beginLocked(wake)
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
	return true
}

// beginLocked claims the loop for one more run. The caller holds a.mu.
func (l *loopState) beginLocked(wake string) model.Loop {
	l.active++
	if l.State != model.LoopStopped {
		l.State = model.LoopRunning
	}
	if wake != "" {
		l.LastWake = wake
	}
	return l.Loop
}

// settleLocked ends one run's claim on the loop's state. The caller holds
// a.mu.
func (l *loopState) settleLocked() {
	if l.active > 0 {
		l.active--
	}
	if l.active == 0 && l.State == model.LoopRunning {
		l.State = model.LoopWaiting
	}
}

// setState moves the loop to state — restarting, stopped — and streams it.
func (l *loopState) setState(a *App, state string) {
	a.mu.Lock()
	if l.State == state {
		a.mu.Unlock()
		return
	}
	l.State = state
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}

// died records that the loop stopped while the product still serves: its
// state is stopped, its last error says why in kit's words, and the Studio
// learns it at once. A stop that was asked for — Stop, a signal — never
// comes here: every caller checks that its context is still live.
func (l *loopState) died(a *App, err error) {
	a.mu.Lock()
	l.State = model.LoopStopped
	l.active = 0
	l.Errors++
	if err != nil {
		_, body := describe(err)
		l.LastError = body.Message
	}
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}

// skipped counts a fire the loop dropped because a run was going.
func (l *loopState) skipped(a *App) {
	a.mu.Lock()
	l.Skipped++
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}

// loopNamed returns the current run's loop called name, or nil.
func (a *App) loopNamed(name string) *loopState {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range a.loops {
		if l.Name == name {
			return l
		}
	}
	return nil
}

// stopLoops marks every loop of the run stopped, once the run is down.
func (a *App) stopLoops() {
	a.mu.Lock()
	loops := slices.Clone(a.loops)
	a.mu.Unlock()
	for _, l := range loops {
		l.setState(a, model.LoopStopped)
	}
}

// loopContext returns ctx carrying the loop label, in dev. Goroutines that
// run under it — and those they start — are the loop's in a profile.
func (a *App) loopContext(ctx context.Context, loop string) context.Context {
	if a.hub == nil || !a.hub.enabled {
		return ctx
	}
	return pprof.WithLabels(ctx, pprof.Labels(labelLoop, loop))
}

// asLoop labels the calling goroutine as the loop's, in dev, and returns
// the context that carries the label.
func (a *App) asLoop(ctx context.Context, loop string) context.Context {
	ctx = a.loopContext(ctx, loop)
	if a.hub != nil && a.hub.enabled {
		pprof.SetGoroutineLabels(ctx)
	}
	return ctx
}

// scheduled wraps a scheduler job so its loop shows the run, and so that a
// fire never overlaps a run the Studio started.
func (a *App) scheduled(l *loopState, wake string, job scheduler.Job) scheduler.Job {
	return func(ctx context.Context) error {
		if !l.tryBegin(a, wake) {
			return errOverlapped
		}
		return job(ctx)
	}
}

// newSchedRunner registers the scheduler's own loop.
func (a *App) newSchedRunner(sched scheduler.Scheduler, entries int) *schedRunner {
	r := &schedRunner{a: a, sched: sched, loop: a.loop("scheduler", "", model.LoopScheduler, schedulerText(entries))}
	a.mu.Lock()
	a.rt.sched = r
	a.mu.Unlock()
	return r
}

// schedulerText says how many entries the scheduler waits on.
func schedulerText(entries int) string {
	if entries == 1 {
		return "earliest of 1 entry"
	}
	return "earliest of " + strconv.Itoa(entries) + " entries"
}

// start runs the scheduler until stop.
//
//ktn:allow-unused-param: the starter interface passes a context this component does not need
func (r *schedRunner) start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = false
	r.runLocked()
	return nil
}

// runLocked starts one Run of the scheduler. The caller holds r.mu.
//
// Goroutine lifecycle: one goroutine runs the scheduler until the context stop
// cancels; it closes done, which stop waits for.
func (r *schedRunner) runLocked() {
	r.armNext()
	ctx, cancel := context.WithCancel(r.a.baseCtx)
	done := make(chan struct{})
	r.cancel, r.done = cancel, done
	go func() {
		defer close(done)
		ctx := r.a.asLoop(ctx, "scheduler")
		if err := r.sched.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error(ctx, r.a.log, "the scheduler stopped", logger.String("error", err.Error()))
			r.a.problem("", say("scheduler.stopped"))
			r.loop.died(r.a, err)
		}
	}()
}

// armNext says when each scheduled loop runs next, as a Run arms it: from
// the clock's time.
func (r *schedRunner) armNext() {
	a := r.a
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	var first *time.Time
	for _, l := range a.loops {
		if l.schedule == nil {
			continue
		}
		if next, ok := l.schedule(now); ok {
			n := next.UTC()
			l.NextRun = &n
			if first == nil || n.Before(*first) {
				first = &n
			}
		}
	}
	r.loop.NextRun = first
}

// stop ends the scheduler and waits for its runs until ctx ends.
func (r *schedRunner) stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fired records one decision of the scheduler on its own loop: every fire
// is a turn of the scheduler's loop.
func (r *schedRunner) fired(res *scheduler.Result, next *time.Time) {
	a := r.a
	a.mu.Lock()
	l := r.loop
	l.Runs++
	at := res.Scheduled.UTC()
	if !res.Started.IsZero() {
		at = res.Started.UTC()
	}
	l.LastRun = &at
	if next != nil {
		n := *next
		if l.NextRun == nil || n.Before(*l.NextRun) || !l.NextRun.After(at) {
			l.NextRun = &n
		}
	}
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}
