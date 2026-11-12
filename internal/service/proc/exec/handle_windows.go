//go:build windows

package exec

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// handle is the live supervision handle for one spawned Windows process. It owns
// the *os.Process, memoises the single Wait outcome, and joins the stdio capture
// copiers so no output is lost. It is safe for concurrent use.
type handle struct {
	proc    *os.Process
	pid     int
	setpgid bool
	stdio   *stdioState
	job     *jobLimit

	waitOnce sync.Once
	waitVal  coreproc.ExitValue
	waitErr  error
	done     chan struct{}
	// settled is closed once Wait has its whole outcome — the exit and the
	// drained (or cut) capture — so WaitContext never returns before it.
	settled chan struct{}

	// clk is what Stop's grace window is measured on: clock.System from
	// newHandle.
	clk clock.Waiter
}

// newHandle wraps a freshly started *os.Process. setpgid records whether the
// child leads its own (console) process group; stdio owns the capture copiers
// Wait joins; job (may be nil) is the rlimit Job Object released once the child
// is reaped.
func newHandle(p *os.Process, setpgid bool, stdio *stdioState, job *jobLimit) *handle {
	//: capture the pid once; Windows reuses pids only after the handle closes.
	return &handle{
		proc: p, pid: p.Pid, setpgid: setpgid, stdio: stdio, job: job,
		done: make(chan struct{}), settled: make(chan struct{}), clk: clock.System,
	}
}

// PID reports the process identifier.
func (h *handle) PID() int {
	//: the pid was captured at spawn and never changes.
	return h.pid
}

// Wait blocks until the process exits and returns its ExitValue. Idempotent: the
// first call reaps and every caller observes the memoised outcome.
func (h *handle) Wait() (exit coreproc.ExitValue, err error) {
	//: the reap runs exactly once; concurrent callers share the memoised result.
	h.waitOnce.Do(func() {
		//: WaitContext observes the whole outcome, never half of it.
		defer close(h.settled)
		state, wErr := h.proc.Wait()
		//: signal late Stop goroutines that the process has been reaped.
		close(h.done)
		//: join the capture copiers so every byte reached the caller's writers,
		//: within the drain grace: a process the child left behind may hold them.
		cut, held, delivered := h.stdio.drainBounded()
		//: release the rlimit Job Object now the child is reaped (no-op when nil),
		//: unless it binds the child's tree to the supervisor's life.
		h.job.releaseAfterExit()
		//: a Wait host fault (not a non-zero exit) is a typed WAIT_FAILED.
		if wErr != nil {
			//: wrap the os.Process.Wait cause under the central WAIT_FAILED fields.
			h.waitErr = wrapWait(wErr, errs.Int("pid", h.pid))
			//: leave waitVal zero on a reap fault.
			return
		}
		//: a clean reap yields the translated exit outcome.
		h.waitVal = exitValueFrom(state)
		//: the grace closed on pipes still held: the exit stands, the drain did not.
		if cut {
			h.waitErr = wrapStdioDrainTimeout(errs.Int("pid", h.pid),
				errs.String("held", strings.Join(held, ",")), errs.Int("bytes", int(delivered)))
			//: the capture error, if any, is secondary to the cut.
			return
		}
		//: a clean exit whose capture writer failed surfaces the typed capture error.
		if cErr := h.stdio.copyError(); cErr != nil {
			//: wrap the writer failure under the central STDIO_CAPTURE_FAILED fields.
			h.waitErr = wrapStdioCapture(cErr, errs.Int("pid", h.pid))
		}
	})
	//: every caller observes the memoised outcome of the single reap.
	return h.waitVal, h.waitErr
}

// exitValueFrom translates an *os.ProcessState into the port's ExitValue. Windows
// has no terminating-signal or wait4 rusage concept, so Signal/Signaled and
// CoreDumped stay zero, MaxRSS is unset and Usage is not Known; only the exit
// code and CPU times are populated.
func exitValueFrom(state *os.ProcessState) coreproc.ExitValue {
	//: ExitCode is -1 for a process killed without a normal exit; the port reads
	//: the code directly and reports CPU times the os layer exposes.
	return coreproc.ExitValue{
		Code:       state.ExitCode(),
		UserTime:   state.UserTime(),
		SystemTime: state.SystemTime(),
	}
}

// WaitContext waits like Wait until the process exits or ctx is done. When
// ctx ends first it returns ctx's error and the child keeps running; a later
// Wait or WaitContext still reports the real exit.
//
// Goroutine lifecycle: it launches reapInBackground, which drives the
// once-only Wait and returns once the child is reaped and its capture drained
// — bounded by the drain grace — whether or not this call is still waiting.
func (h *handle) WaitContext(ctx context.Context) (exit coreproc.ExitValue, err error) {
	//: a settled handle answers at once, whatever ctx says.
	select {
	case <-h.settled:
		//: the memoised outcome.
		return h.waitVal, h.waitErr
	default:
	}
	//: an expired ctx returns before any goroutine starts.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the child keeps running.
		return coreproc.ExitValue{}, ctxErr
	}
	//: drive the reap in the background so this select can leave on ctx.
	go h.reapInBackground()
	//: race the whole outcome against ctx.
	select {
	case <-h.settled:
		//: the memoised outcome.
		return h.waitVal, h.waitErr
	case <-ctx.Done():
		//: the caller stops waiting; the child does not stop.
		return coreproc.ExitValue{}, ctx.Err()
	}
}

// Signal delivers sig to the process. Windows os.Process.Signal honours only
// os.Kill; other signals are rejected by the runtime, surfaced as SIGNAL_FAILED.
func (h *handle) Signal(sig coreproc.Signal) error {
	//: bridge the typed Signal to the os.Signal the Windows runtime understands.
	if err := h.proc.Signal(sig.OS()); err != nil {
		//: wrap the rejection under the central SIGNAL_FAILED fields.
		return wrapSignal(err, errs.Int("pid", h.pid), errs.Int("signal", sig.Int()))
	}
	//: the signal was accepted for delivery.
	return nil
}

// SignalGroup delivers sig to a Setpgid child's console process group as its
// console control event (#933): SIGINT, SIGTERM, SIGHUP and SIGQUIT are each
// CTRL_BREAK_EVENT, the one event Windows delivers to a single group, so the
// group's handlers — a Go child's os/signal, which reads it as os.Interrupt,
// included — get a real chance to exit. SIGKILL, and every
// signal to a child without its own group, reach the leader alone through
// Signal: CTRL_BREAK to the supervisor's own console group would reach the
// supervisor.
func (h *handle) SignalGroup(sig coreproc.Signal) error {
	event, mapped := consoleEvent(sig)
	//: a private console group and a signal it has an event for.
	if h.setpgid && mapped {
		//: the event goes to the group the child leads.
		return sendConsoleEvent(event, h.pid, sig)
	}
	//: no group of its own, or SIGKILL: the leader process alone.
	return h.Signal(sig)
}

// Stop sends sig, waits up to grace for exit, then force-terminates. A Setpgid
// child gets sig as its console control event (SignalGroup), so the grace is a
// real window for a graceful exit; a child without its own group gets the
// best-effort os.Process.Signal, which Windows refuses for anything but a
// kill. The forced Kill (TerminateProcess) is the reliable escalation.
func (h *handle) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	//: best-effort graceful nudge: the console event for a private group.
	swallowErr(h.SignalGroup(sig))
	//: wait up to grace for a voluntary exit before escalating.
	settled, err := h.awaitExit(ctx, grace)
	//: a cancelled context surfaces verbatim.
	if err != nil {
		//: propagate the context error to the caller.
		return err
	}
	//: a voluntary exit within grace needs no forced terminate.
	if settled {
		//: the process is already gone.
		return nil
	}
	//: escalate to a forced terminate (TerminateProcess via os.Process.Kill).
	if kerr := h.proc.Kill(); kerr != nil {
		//: wrap the terminate failure under the central STOP_FAILED fields.
		return wrapStop(kerr, errs.Int("pid", h.pid))
	}
	//: reap the killed process so it does not linger.
	_, werr := h.Wait()
	//: surface a reap fault; a clean kill+reap returns nil.
	return werr
}

// awaitExit waits up to grace for the process to be reaped (Wait closes done),
// returning whether it settled. A cancelled ctx returns its error.
func (h *handle) awaitExit(ctx context.Context, grace time.Duration) (settled bool, err error) {
	//: drive the single Wait in the background so done closes on exit.
	go h.reapInBackground()
	//: the grace window runs on the handle's clock, as on Unix.
	timer := h.clk.NewTimer(grace)
	defer timer.Stop()
	//: race exit against the grace deadline and context cancellation.
	select {
	//: the process exited within grace.
	case <-h.done:
		//: settled cleanly — no forced terminate needed.
		return true, nil
	//: the grace window elapsed without exit.
	case <-timer.C():
		//: not settled — the caller escalates to a forced terminate.
		return false, nil
	//: the caller cancelled the stop.
	case <-ctx.Done():
		//: surface the cancellation as the stop error.
		return false, ctx.Err()
	}
}

// reapInBackground drives the once-only Wait so a select can observe h.done
// and h.settled; the outcome is memoised for whoever calls Wait.
func (h *handle) reapInBackground() {
	//: the reap fault, if any, is already memoised in h.waitErr.
	if _, err := h.Wait(); err != nil {
		//: nothing to propagate from here.
		return
	}
}
