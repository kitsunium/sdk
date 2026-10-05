package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// NewSupervisor builds a supervisor named name over run, tuned by cfg. run
// must run until its context ends and then return; returning earlier — with
// an error, with nil, or by panicking — is a failure, and run is started again
// after the backoff. NewSupervisor starts nothing: Start does.
//
//	janitor, err := lifecycle.NewSupervisor("janitor", sweep, lifecycle.SupervisorConfig{})
//	err = app.Add(janitor.Component()) // started and stopped with the application
func NewSupervisor(name string, run func(ctx context.Context) error, cfg SupervisorConfig) (*Supervisor, error) {
	//: a name and a function, or nothing to supervise.
	if invalid := validateSupervisor(name, run); invalid != nil {
		//: SupervisorMisconfigured.
		return nil, invalid
	}
	clk, backoff, healthy := cfg.resolved()
	//: stopped until Start.
	return &Supervisor{run: run, clock: clk, observe: cfg.Observe, name: name, backoff: backoff, healthy: healthy}, nil
}

// Start begins the supervision on a goroutine of its own and returns at once.
//
// ctx is where every run's context comes from: its values reach each run —
// a trace, a request-scoped logger, pprof labels — and its cancellation does
// NOT end the supervision, because ctx is the START's, and a Lifecycle hands
// a component the context of its startup, which must be able to end without
// ending what it started. Stop ends the supervision. A ctx already done is
// refused with its own error, as a Start that is being aborted.
//
// A second Start while a supervision is running is SupervisorRunning.
func (s *Supervisor) Start(ctx context.Context) error {
	//: a start that was aborted before it began.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the caller's own verdict.
		return ctxErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: one supervision at a time.
	if s.running() {
		//: SupervisorRunning, naming the supervisor.
		return kerrs.Wrap(corelc.SupervisorRunning, kerrs.WrapParams{}, kerrs.String("supervisor", s.name))
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	//: Goroutine lifecycle: one per supervision, ended by Stop through
	//: cancel, and joined by Stop through done.
	go s.supervise(runCtx, done)
	//: supervising.
	return nil
}

// Stop ends the supervision: it cancels the running function's context and
// waits for the function to return and the supervision to end. ctx bounds the
// wait: when it ends first, Stop returns StopTimeout and the function is left
// running with its context cancelled — nothing is killed, and a later Stop
// waits again. A supervisor that is not running returns nil.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	//: never started.
	if done == nil {
		//: nothing to stop.
		return nil
	}
	cancel()
	select {
	//: the function returned and the supervision ended.
	case <-done:
		//: stopped.
		return nil
	//: the caller stopped waiting first.
	case <-ctx.Done():
		//: StopTimeout, the lifecycle's verdict for a Stop that overran.
		return kerrs.Wrap(corelc.StopTimeout, kerrs.WrapParams{}, kerrs.String("component", s.name))
	}
}

// component is Supervisor.Component's body: decl_gen.go writes Supervisor.Component, from the
// design, as one call of it.
func (s *Supervisor) component() corelc.ComponentValue {
	//: the three fields a component is.
	return corelc.ComponentValue{Name: s.name, Start: s.Start, Stop: s.Stop}
}

// running reports whether a supervision started and has not ended. The
// caller holds mu.
func (s *Supervisor) running() bool {
	//: never started.
	if s.done == nil {
		//: not running.
		return false
	}
	select {
	//: the last supervision ended, by Stop or by itself.
	case <-s.done:
		//: not running.
		return false
	//: still going.
	default:
		//: running.
		return true
	}
}

// supervise is the supervision: run, and after every early end, back off and
// run again, until ctx ends.
func (s *Supervisor) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)
	failures := 0
	//: one iteration per run.
	for run := 1; ; run++ {
		started := s.clock.Now()
		s.emit(SupervisionEventValue{Phase: SupervisionRunStarted, At: started, Run: run})
		runErr := s.once(ctx)
		ended := s.clock.Now()
		//: the supervision is stopping: the run's end is the way out.
		if ctx.Err() != nil {
			s.emit(SupervisionEventValue{
				Phase: SupervisionRunEnded, At: ended, Run: run,
				Err: wayOut(runErr), Duration: ended.Sub(started), Stopping: true,
			})
			s.emit(SupervisionEventValue{Phase: SupervisionStopped, At: ended, Run: run})
			return
		}
		s.emit(SupervisionEventValue{Phase: SupervisionRunEnded, At: ended, Run: run, Err: runErr, Duration: ended.Sub(started)})
		//: a run that lasted was working: its end starts the count again.
		if ended.Sub(started) >= s.healthy {
			failures = 0
		}
		failures++
		delay := s.backoff.Delay(failures)
		s.emit(SupervisionEventValue{Phase: SupervisionRestarting, At: ended, Run: run, Failures: failures, Delay: delay})
		//: stopped during the backoff: no further run.
		if !s.wait(ctx, delay) {
			s.emit(SupervisionEventValue{Phase: SupervisionStopped, At: s.clock.Now(), Run: run})
			return
		}
	}
}

// once runs the function once and turns a panic into RunPanicked.
func (s *Supervisor) once(ctx context.Context) (err error) {
	defer func() {
		recovered := recover()
		//: the ordinary path.
		if recovered == nil {
			return
		}
		//: the value and the stack as fields, never the origin and never the
		//: Public text.
		err = kerrs.Wrap(corelc.RunPanicked, kerrs.WrapParams{},
			kerrs.String("supervisor", s.name), kerrs.String("panic", fmt.Sprint(recovered)),
			kerrs.String("stack", string(debug.Stack())))
	}()
	//: the caller's loop.
	return s.run(ctx)
}

// wait waits delay on the supervisor's clock, and reports whether the
// supervision should carry on — false when ctx ended first.
func (s *Supervisor) wait(ctx context.Context, delay time.Duration) bool {
	timer := s.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	//: stopped during the backoff.
	case <-ctx.Done():
		//: no further run.
		return false
	//: the backoff elapsed.
	case <-timer.C():
		//: run again.
		return true
	}
}

// emit tells the observer, when there is one.
func (s *Supervisor) emit(event SupervisionEventValue) {
	//: nobody asked.
	if s.observe == nil {
		return
	}
	event.Name = s.name
	s.observe(event)
}

// wayOut is the error a run that ended with its supervision reports: its
// context's own error is the way out, not a failure, and anything else it
// returned — a panic included — is kept.
func wayOut(runErr error) error {
	//: the run honoured its context.
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		//: a clean end.
		return nil
	}
	//: what it said on the way out.
	return runErr
}
