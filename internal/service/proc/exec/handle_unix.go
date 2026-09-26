//go:build unix

// Package exec — the process-handle value: a Unix supervision handle
// implementing the coreproc.Process port over an *os.Process, with group-aware
// Signal/Stop and a once-only Wait that captures exit status plus wait4 rusage.
// The concrete type is unexported; Start returns it as the coreproc.Process
// interface.
package exec

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/childwait"
)

// signalledCode is the ExitValue.Code reported for a signalled (non-normal)
// termination; the port reserves -1 for "died by signal, no status".
const signalledCode int = -1

// maxRSSKB (in maxrss_rss64_unix.go / maxrss_rss32_unix.go) reports the peak
// resident set size in kilobytes from the wait4 rusage. Rusage.Maxrss is int64
// on a 64-bit GOARCH but int32 on 386/arm, so the widening lives in a per-width
// file: the 64-bit path passes the value through (no redundant cast), the 32-bit
// path widens to int64.

// handle is the live supervision handle for one spawned process. It owns the
// *os.Process and the claim on its exit status, remembers the leader pid and
// process-group id, and memoises the single Wait outcome so
// PID/Wait/Signal/SignalGroup/Stop satisfy the coreproc.Process port. It is
// safe for concurrent use.
type handle struct {
	proc    *os.Process
	claim   *childwait.Claim
	pid     int
	pgid    int
	setpgid bool
	stdio   *stdioState

	// procMu serialises the leader-only Signal against the release of proc
	// after a reaper sweep took its status: os.Process.Release writes the Pid
	// field that a pid-mode os.Process.Signal reads.
	procMu sync.RWMutex

	waitOnce sync.Once
	waitVal  coreproc.ExitValue
	waitErr  error
	done     chan struct{}
}

// newHandle wraps a freshly started *os.Process and the claim on its exit
// status as a handle. setpgid records whether the child leads its own process
// group (Spec.Setpgid): only then is the leader pid a valid pgid for
// group-directed kills. stdio owns the capture copiers that Wait joins so no
// output is lost and no goroutine leaks.
func newHandle(p *os.Process, claim *childwait.Claim, setpgid bool, stdio *stdioState) *handle {
	//: with Setpgid the leader pid IS the group id; without it there is no private
	//: group and group operations degrade to the leader (see groupTarget).
	return &handle{proc: p, claim: claim, pid: p.Pid, pgid: p.Pid, setpgid: setpgid, stdio: stdio, done: make(chan struct{})}
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
// idempotent: the first call collects the exit status and every caller observes
// the same memoised outcome. The status is the child's own even when a running
// reaper collected the child first (ADR 0093).
func (h *handle) Wait() (exit coreproc.ExitValue, err error) {
	//: the reap runs exactly once under sync.Once; concurrent callers share it.
	//: the close(h.done) below is therefore executed at most once — the Once is
	//: the guard against a double-close panic.
	h.waitOnce.Do(func() {
		ev, swept, wErr := collectExit(h.proc, h.claim)
		//: a swept status leaves proc unwaited: free its handle now.
		if swept {
			h.releaseProc()
		}
		//: signal late Stop goroutines that the process has been reaped.
		close(h.done)
		//: join the capture copiers so every byte the child wrote has reached the
		//: caller's writers before Wait returns, and no copier goroutine leaks.
		//: the child's exit closed its write ends, so the copiers drain to EOF.
		h.stdio.wait()
		//: a status nobody could observe (not a non-zero exit) is a typed WAIT_FAILED.
		if wErr != nil {
			//: wrap the cause under the central WAIT_FAILED fields.
			h.waitErr = wrapWait(wErr, errs.Int("pid", h.pid))
			//: leave waitVal at its zero value on a reap fault.
			return
		}
		//: a collected status yields the translated exit outcome.
		h.waitVal = ev
		//: a clean exit whose capture writer failed surfaces the typed capture
		//: error (os/exec semantics) — the exit status still stands in waitVal.
		if cErr := h.stdio.copyError(); cErr != nil {
			//: wrap the writer failure under the central STDIO_CAPTURE_FAILED fields.
			h.waitErr = wrapStdioCapture(cErr, errs.Int("pid", h.pid))
		}
	})
	//: every caller observes the memoised outcome of the single reap.
	return h.waitVal, h.waitErr
}

// waiter is what collectExit needs from the process it reaps: its own wait,
// and nothing else. Production hands it an *os.Process; the narrower type is
// the statement that collecting an exit status neither signals nor releases
// the process — the caller decides that from the swept flag.
type waiter interface {
	// Wait blocks until the process exits and returns its state.
	Wait() (*os.ProcessState, error)
}

// releaser is what an aborted spawn needs from the child it abandons: its own
// wait, to reap it, and the release of its handle when a sweep reaped it
// first. Production hands it an *os.Process.
type releaser interface {
	waiter
	// Release frees the process handle without waiting for the process.
	Release() error
}

// collectExit obtains proc's exit status from whichever waiter took it, and
// ends the claim. Without a running reaper that is always proc's own wait, and
// nothing changes from a plain os.Process.Wait. With one, a sweep may collect
// the child first; its status is then on the claim — before the wait, when the
// sweep came first, or after the wait failed with ECHILD, once the sweep's
// hand-off has landed. swept reports that case: proc's own Wait never
// completed, so the caller must Release proc. err is the wait's own error only
// when the status is truly unobservable: something outside the SDK reaped the
// child, or wait4 failed for another reason.
func collectExit(proc waiter, claim *childwait.Claim) (exit coreproc.ExitValue, swept bool, err error) {
	//: a sweep already took the child: do not wait by pid for a process that is
	//: gone, whose pid may by now be another process's.
	if status, ok := claim.Collected(); ok {
		//: the status the sweep collected is the child's own.
		return exitValue(status.WaitStatus, &status.Rusage), true, nil
	}
	state, wErr := proc.Wait()
	//: our own wait took it — the only outcome when no reaper runs.
	if wErr == nil {
		//: the claim can no longer be filled; drop it from the ledger.
		claim.Release()
		//: translate the status our wait collected.
		return exitValueFrom(state), false, nil
	}
	//: ECHILD: somebody else collected the child. If it was the reaper, the
	//: status is on the claim once the sweep's hand-off is done.
	if errors.Is(wErr, syscall.ECHILD) {
		//: a status the sweep handed over is the child's own.
		if status, ok := claim.Reclaim(); ok {
			//: translate the status the sweep collected.
			return exitValue(status.WaitStatus, &status.Rusage), true, nil
		}
	}
	//: the status is lost or the wait faulted; either way the claim is done.
	claim.Release()
	//: hand back the wait's own error for the caller to type.
	return coreproc.ExitValue{}, false, wErr
}

// releaseProc frees the handle the runtime keeps for proc (a pidfd on Linux)
// once a reaper sweep took its status: proc's own Wait never completed, so
// nothing else would release it before a garbage collection. It excludes a
// concurrent leader-only Signal, which reads what Release writes.
func (h *handle) releaseProc() {
	//: no Signal may be inside os.Process while it is released.
	h.procMu.Lock()
	//: reopen Signal once the handle is released.
	defer h.procMu.Unlock()
	//: Release cannot fail on Unix; nothing waits on or signals proc again.
	swallowErr(h.proc.Release())
}

// leaderReaped reports whether the leader's exit status has been collected —
// by Wait, or by a reaper sweep that handed it over. Its pid may by then belong
// to another process, so nothing may be sent to it by number.
func (h *handle) leaderReaped() bool {
	//: Wait has run to completion.
	select {
	//: done is closed once the status is in hand.
	case <-h.done:
		//: reaped.
		return true
	//: Wait has not completed; a sweep may still have taken the child.
	default:
	}
	_, collected := h.claim.Collected()
	//: a status on the claim means a sweep reaped the leader.
	return collected
}

// Signal delivers sig to the leader process only (kill(pid, sig)). A leader
// already reaped — by Wait or by a reaper sweep — is reported finished and
// never signalled by pid: that pid may by now be another process's.
func (h *handle) Signal(sig coreproc.Signal) error {
	//: hold off releaseProc for the whole check-then-signal.
	h.procMu.RLock()
	//: let a release proceed once this signal is delivered or refused.
	defer h.procMu.RUnlock()
	//: a reaped leader is gone, whoever collected its status.
	if h.leaderReaped() {
		//: the same outcome os.Process gives a signal after its own Wait.
		return wrapSignal(os.ErrProcessDone, errs.Int("pid", h.pid), errs.Int("signal", sig.Int()))
	}
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
// inherited group — and once that leader is reaped it reports ESRCH rather
// than signal a pid another process may now hold.
func (h *handle) SignalGroup(sig coreproc.Signal) error {
	//: resolve the group target (-pgid with Setpgid, else the leader pid).
	target := h.groupTarget()
	//: without a private group the target is the leader's pid, which may belong
	//: to another process once the leader is reaped: report it gone instead. A
	//: private group is still addressed — it can outlive its leader.
	if !h.setpgid && h.leaderReaped() {
		//: the same verdict kill(2) gives a pid that no longer exists.
		return wrapSignal(syscall.ESRCH, errs.Int("target", target), errs.Int("signal", sig.Int()))
	}
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
	ru, _ := state.SysUsage().(*syscall.Rusage)
	//: the exit-vs-signal distinction lives in the Unix WaitStatus.
	if ws, ok := state.Sys().(syscall.WaitStatus); ok {
		//: translate the status word and the usage together.
		return exitValue(ws, ru)
	}
	//: no status word: only the usage can be reported, the code stays -1.
	out := coreproc.ExitValue{Code: signalledCode}
	//: CPU time and peak RSS live in the wait4 rusage when the platform supplies it.
	if ru != nil {
		//: translate the rusage accounting into the ExitValue.
		readRusage(ru, &out)
	}
	//: the exit outcome that could be read.
	return out
}

// exitValue converts a wait4 status word and its rusage into the port's
// ExitValue — the one translation for a status collected by the handle's own
// wait and for one a reaper sweep handed over. ru may be nil.
func exitValue(ws syscall.WaitStatus, ru *syscall.Rusage) coreproc.ExitValue {
	out := coreproc.ExitValue{Code: signalledCode}
	//: a normal exit reports a 0–255 status code.
	if ws.Exited() {
		out.Code = ws.ExitStatus()
	}
	//: a signalled death records the terminating signal, code stays -1.
	if ws.Signaled() {
		out.Signaled = true
		out.Signal = coreproc.Signal(ws.Signal())
	}
	//: CPU time and peak RSS live in the wait4 rusage when the platform supplies it.
	if ru != nil {
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
