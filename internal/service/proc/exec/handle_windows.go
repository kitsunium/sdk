//go:build windows

// Package exec — the Windows process-handle value implementing the
// coreproc.Process port over an *os.Process. Windows has no Unix process groups
// or wait4 rusage, so SignalGroup degrades to the leader and the ExitValue
// carries the exit code + CPU times the os layer exposes (no terminating signal,
// no MaxRSS). The concrete type is unexported; Start returns it as the interface.
package exec

import (
	"context"
	"os"
	"sync"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
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
}

// newHandle wraps a freshly started *os.Process. setpgid records whether the
// child leads its own (console) process group; stdio owns the capture copiers
// Wait joins; job (may be nil) is the rlimit Job Object released once the child
// is reaped.
func newHandle(p *os.Process, setpgid bool, stdio *stdioState, job *jobLimit) *handle {
	//: capture the pid once; Windows reuses pids only after the handle closes.
	return &handle{proc: p, pid: p.Pid, setpgid: setpgid, stdio: stdio, job: job, done: make(chan struct{})}
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
		state, wErr := h.proc.Wait()
		//: signal late Stop goroutines that the process has been reaped.
		close(h.done)
		//: join the capture copiers so every byte reached the caller's writers.
		h.stdio.wait()
		//: release the rlimit Job Object now the child is reaped (no-op when nil).
		h.job.close()
		//: a Wait host fault (not a non-zero exit) is a typed WAIT_FAILED.
		if wErr != nil {
			//: wrap the os.Process.Wait cause under the central WAIT_FAILED fields.
			h.waitErr = wrapWait(wErr, errs.Int("pid", h.pid))
			//: leave waitVal zero on a reap fault.
			return
		}
		//: a clean reap yields the translated exit outcome.
		h.waitVal = exitValueFrom(state)
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
// has no terminating-signal or wait4 rusage concept, so Signal/Signaled stay zero
// and MaxRSS is unset; only the exit code and CPU times are populated.
func exitValueFrom(state *os.ProcessState) coreproc.ExitValue {
	//: ExitCode is -1 for a process killed without a normal exit; the port reads
	//: the code directly and reports CPU times the os layer exposes.
	return coreproc.ExitValue{
		Code:       state.ExitCode(),
		UserTime:   state.UserTime(),
		SystemTime: state.SystemTime(),
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

// SignalGroup degrades to the leader on Windows: there is no kill(-pgid). Console
// process-group delivery (CTRL_BREAK) is the signal package's concern; here a
// group request reaches the leader process so the contract never silently no-ops.
func (h *handle) SignalGroup(sig coreproc.Signal) error {
	//: no Unix process-group fan-out exists — target the leader process.
	return h.Signal(sig)
}

// Stop sends sig, waits up to grace for exit, then force-terminates. Windows has
// no graceful SIGTERM, so a non-Kill sig is best-effort and the forced Kill
// (TerminateProcess) is the reliable escalation.
func (h *handle) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	//: best-effort graceful nudge; Windows may reject a non-Kill signal.
	swallowErr(h.proc.Signal(sig.OS()))
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
	go func() {
		//: the memoised Wait closes done; a second call here is harmless.
		_, _ = h.Wait()
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	//: race exit against the grace deadline and context cancellation.
	select {
	//: the process exited within grace.
	case <-h.done:
		//: settled cleanly — no forced terminate needed.
		return true, nil
	//: the grace window elapsed without exit.
	case <-timer.C:
		//: not settled — the caller escalates to a forced terminate.
		return false, nil
	//: the caller cancelled the stop.
	case <-ctx.Done():
		//: surface the cancellation as the stop error.
		return false, ctx.Err()
	}
}
