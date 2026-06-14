//go:build unix

// Package exec — the process-handle value: a Unix supervision handle
// implementing the coreproc.Process port over an *os.Process, with group-aware
// Signal/Stop and a once-only Wait that captures exit status plus wait4 rusage.
// The concrete type is unexported; Start returns it as the coreproc.Process
// interface.
package exec

import (
	"context"
	"os"
	"sync"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// signalledCode is the ExitValue.Code reported for a signalled (non-normal)
// termination; the port reserves -1 for "died by signal, no status".
const signalledCode int = -1

// maxRSSKB reports the peak resident set size in kilobytes from the wait4
// rusage. ru.Maxrss is already int64 on every Unix target, so it is read
// directly without a conversion.
func maxRSSKB(ru *syscall.Rusage) int64 {
	//: the kernel reports peak RSS in kilobytes on Unix; pass it through.
	return ru.Maxrss
}

// handle is the live supervision handle for one spawned process. It owns the
// *os.Process, remembers the leader pid and process-group id, and memoises the
// single Wait outcome so PID/Wait/Signal/SignalGroup/Stop satisfy the
// coreproc.Process port. It is safe for concurrent use.
type handle struct {
	proc    *os.Process
	pid     int
	pgid    int
	setpgid bool
	stdio   *stdioState

	waitOnce sync.Once
	waitVal  coreproc.ExitValue
	waitErr  error
	done     chan struct{}
}

// newHandle wraps a freshly started *os.Process as a handle. setpgid records
// whether the child leads its own process group (Spec.Setpgid): only then is the
// leader pid a valid pgid for group-directed kills. stdio owns the capture
// copiers that Wait joins so no output is lost and no goroutine leaks.
func newHandle(p *os.Process, setpgid bool, stdio *stdioState) *handle {
	//: with Setpgid the leader pid IS the group id; without it there is no private
	//: group and group operations degrade to the leader (see groupTarget).
	return &handle{proc: p, pid: p.Pid, pgid: p.Pid, setpgid: setpgid, stdio: stdio, done: make(chan struct{})}
}

// groupTarget returns the kill(2) target for group-directed operations: the
// negated pgid when the child leads its own group (Setpgid), otherwise the leader
// pid. Without a private group, -pgid would address no group (ESRCH, silently
// swallowed) or, worse, the supervisor's own inherited group — so a non-grouped
// child is only ever signalled at its leader.
func (h *handle) groupTarget() int {
	//: a private group is addressable as -pgid in a single kill(2).
	if h.setpgid {
		//: negative pid fans the signal out to every group member.
		return -h.pgid
	}
	//: no private group — degrade to the single leader pid.
	return h.pid
}

// PID reports the leader process identifier.
func (h *handle) PID() int {
	//: the leader pid was captured at spawn and never changes.
	return h.pid
}

// Wait blocks until the process exits and returns its ExitValue. It is
// idempotent: the first call performs the wait4 reap and every caller observes
// the same memoised outcome.
func (h *handle) Wait() (exit coreproc.ExitValue, err error) {
	//: the reap runs exactly once under sync.Once; concurrent callers share it.
	//: the close(h.done) below is therefore executed at most once — the Once is
	//: the guard against a double-close panic.
	h.waitOnce.Do(func() {
		state, wErr := h.proc.Wait()
		//: signal late Stop goroutines that the process has been reaped.
		close(h.done)
		//: join the capture copiers so every byte the child wrote has reached the
		//: caller's writers before Wait returns, and no copier goroutine leaks.
		//: the child's exit closed its write ends, so the copiers drain to EOF.
		h.stdio.wait()
		//: a wait4 host fault (not a non-zero exit) is a typed WAIT_FAILED.
		if wErr != nil {
			//: wrap the os.Process.Wait cause under the central WAIT_FAILED fields.
			h.waitErr = wrapWait(wErr, errs.Int("pid", h.pid))
			//: leave waitVal at its zero value on a reap fault.
			return
		}
		//: a clean reap yields the translated exit outcome.
		h.waitVal = exitValueFrom(state)
	})
	//: every caller observes the memoised outcome of the single reap.
	return h.waitVal, h.waitErr
}

// Signal delivers sig to the leader process only (kill(pid, sig)).
func (h *handle) Signal(sig coreproc.Signal) error {
	//: a leader-only signal targets the single pid via the os.Process bridge.
	if err := h.proc.Signal(sig.OS()); err != nil {
		//: wrap the kill(2) cause under the central SIGNAL_FAILED fields.
		return wrapSignal(err, errs.Int("pid", h.pid), errs.Int("signal", sig.Int()))
	}
	//: the signal was accepted by the kernel for delivery.
	return nil
}

// SignalGroup delivers sig to the leader's entire process group via
// kill(-pgid, sig) when the child leads its own group (Spec.Setpgid), reaching
// forked grandchildren (the control-group analogue). Without a private group it
// degrades to the leader process alone, so it never signals the supervisor's
// inherited group.
func (h *handle) SignalGroup(sig coreproc.Signal) error {
	//: resolve the group target (-pgid with Setpgid, else the leader pid).
	target := h.groupTarget()
	//: one kill(2) reaches the whole group (negative) or the leader (positive).
	if err := syscall.Kill(target, syscall.Signal(sig.Int())); err != nil {
		//: wrap the group kill(2) cause under the central SIGNAL_FAILED fields.
		return wrapSignal(err, errs.Int("target", target), errs.Int("signal", sig.Int()))
	}
	//: the signal was accepted for delivery to the resolved target.
	return nil
}

// Stop terminates the process group gracefully: it signals the whole group with
// sig, waits up to grace for exit, then escalates to SIGKILL on the group. It
// returns nil once the group has exited, ctx's error if cancelled first, or
// StopFailed if the group survives the SIGKILL escalation.
func (h *handle) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	//: phase one — request graceful termination of the entire group.
	if err := h.signalGroupAllowGone(sig); err != nil {
		//: a real delivery failure (not "already gone") is reported verbatim.
		return err
	}

	//: wait for graceful exit, ctx cancellation, or the grace deadline.
	settled, gracefulErr := h.awaitExit(ctx, grace)
	//: a settled outcome (exited or ctx done) needs no escalation.
	if settled {
		//: return the graceful result without sending SIGKILL.
		return gracefulErr
	}

	//: phase two — grace elapsed; escalate to an unignorable group SIGKILL.
	if err := h.signalGroupAllowGone(coreproc.Signal(syscall.SIGKILL)); err != nil {
		//: a real escalation failure means the group could not be stopped.
		return wrapStop(err, errs.Int("pgid", h.pgid))
	}

	//: SIGKILL is unignorable — wait unbounded (bar ctx) for the final exit.
	_, finalErr := h.awaitExit(ctx, 0)
	//: return whatever the post-SIGKILL wait observed (exit or ctx cancel).
	return finalErr
}

// signalGroupAllowGone delivers sig to the group but treats an already-exited
// group (ESRCH) as success, since a vanished target needs no further signal.
func (h *handle) signalGroupAllowGone(sig coreproc.Signal) error {
	err := h.SignalGroup(sig)
	//: a group already gone (ESRCH) is success — nothing left to signal.
	if err != nil && processGone(err) {
		//: swallow the benign "no such process" race.
		return nil
	}
	//: any other outcome (success or a real fault) passes through unchanged.
	return err
}

// awaitExit blocks until the process is reaped, ctx is cancelled, or (when grace
// is positive) the grace timer fires. settled reports whether a terminal
// outcome was reached; when false the caller must escalate.
//
// Goroutine lifecycle: it launches reapInBackground, which drives the once-only
// Wait and returns as soon as the reap completes (closing h.done). The goroutine
// cannot outlive the process: it blocks only inside os.Process.Wait, which
// returns when the child is reaped, so no leak survives this call.
func (h *handle) awaitExit(ctx context.Context, grace time.Duration) (settled bool, err error) {
	//: reap in the background so this select observes completion via h.done.
	go h.reapInBackground()

	//: a non-positive grace means "wait without a deadline" (post-SIGKILL path).
	if grace <= 0 {
		//: block on exit or ctx only — no timer arms.
		select {
		case <-h.done:
			//: the process exited; the outcome is terminal.
			return true, nil
		case <-ctx.Done():
			//: the caller cancelled the wait before exit.
			return true, ctx.Err()
		}
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	//: race exit against ctx and the grace deadline.
	select {
	case <-h.done:
		//: graceful exit within the window — terminal success.
		return true, nil
	case <-ctx.Done():
		//: ctx cancelled before the process exited — terminal.
		return true, ctx.Err()
	case <-timer.C:
		//: grace elapsed without exit — not settled; caller must escalate.
		return false, nil
	}
}

// reapInBackground triggers the once-only Wait so Stop's select can observe
// h.done without leaking a blocked goroutine. Any reap error is intentionally
// not returned here: it is memoised in h.waitErr by Wait itself and surfaced to
// whoever calls h.Wait() directly, so this driver has nowhere to propagate it.
func (h *handle) reapInBackground() {
	//: drive the reap; the outcome is observed via h.done and a later Wait call.
	if _, err := h.Wait(); err != nil {
		//: the reap fault is already memoised in h.waitErr for the real caller.
		return
	}
}

// exitValueFrom converts a finished *os.ProcessState into the port's ExitValue,
// reading exit status / terminating signal from WaitStatus and CPU/RSS from the
// wait4 rusage.
func exitValueFrom(state *os.ProcessState) coreproc.ExitValue {
	out := coreproc.ExitValue{Code: signalledCode}
	//: the exit-vs-signal distinction lives in the Unix WaitStatus; read it inline
	//: so no helper takes the concrete kernel type as a parameter.
	if ws, ok := state.Sys().(syscall.WaitStatus); ok {
		//: a normal exit reports a 0–255 status code.
		if ws.Exited() {
			out.Code = ws.ExitStatus()
		}
		//: a signalled death records the terminating signal, code stays -1.
		if ws.Signaled() {
			out.Signaled = true
			out.Signal = coreproc.Signal(ws.Signal())
		}
	}
	//: CPU time and peak RSS live in the wait4 rusage when the platform supplies it.
	if ru, ok := state.SysUsage().(*syscall.Rusage); ok && ru != nil {
		//: translate the rusage accounting into the ExitValue.
		readRusage(ru, &out)
	}
	//: a fully-populated exit outcome for Wait to memoise.
	return out
}

// readRusage fills out's CPU times and MaxRSS from the wait4 rusage ru.
func readRusage(ru *syscall.Rusage, out *coreproc.ExitValue) {
	//: combine user/system CPU time and peak RSS into the ExitValue.
	out.UserTime = timevalToDuration(ru.Utime)
	out.SystemTime = timevalToDuration(ru.Stime)
	out.MaxRSS = maxRSSKB(ru)
}

// timevalToDuration converts a syscall.Timeval (seconds + microseconds) to a
// time.Duration.
func timevalToDuration(tv syscall.Timeval) time.Duration {
	//: combine the whole-second and microsecond halves into one Duration.
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// processGone reports whether err's root cause is the benign "no such process"
// condition (ESRCH) — the group already exited.
func processGone(err error) bool {
	//: ESRCH from a group kill means the target is already gone — treat as done.
	return errs.HasCode(err, coreproc.CodeSignalFailed) && containsESRCH(err)
}
