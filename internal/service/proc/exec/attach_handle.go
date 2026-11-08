//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"context"
	"slices"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// unknownExitCode is the Code of the ExitValue an attached process's Wait
// returns: no status is known, and a zero would read as success.
const unknownExitCode int = -1

// attachPIDFile reads the PID file, refuses what it may not adopt, and
// adopts the rest.
func attachPIDFile(path string, want coreproc.AttachValue) (coreproc.Process, error) {
	pid, trusted, err := readPIDFile(path)
	//: the file could not be read safely.
	if err != nil {
		//: PID_FILE_REFUSED.
		return nil, err
	}
	//: init and this process, whatever the file.
	if err = refuseSelfOrInit(pid); err != nil {
		//: ADOPTION_REFUSED.
		return nil, err
	}
	//: a file another user could have written, and nothing to check its PID by.
	if !trusted && !bounded(want) {
		//: ADOPTION_REFUSED naming the file.
		return nil, wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.String("path", path),
			errs.String("why", "the PID file is not trustworthy and no cgroup or user bounds the process"))
	}
	//: the adoption, under want's bounds.
	return attachProcess(pid, want)
}

// checkUsers checks the users bound of want against the real and effective
// uid read of the process pid; readErr is the read's fault, which refuses.
func checkUsers(pid int, want coreproc.AttachValue, ruid, euid int, readErr error) error {
	//: no user bound.
	if len(want.UIDs) == 0 {
		//: nothing to check.
		return nil
	}
	//: unreadable, or a user outside the set.
	if readErr != nil || !slices.Contains(want.UIDs, ruid) || !slices.Contains(want.UIDs, euid) {
		//: ADOPTION_REFUSED naming the users.
		return wrapAdoptionRefused(readErr, errs.Int("pid", pid), errs.Int("uid", ruid), errs.Int("euid", euid))
	}
	//: within the bound.
	return nil
}

// PID reports the process identifier the handle was attached from.
func (a *adopted) PID() int {
	//: the number the handle was opened from.
	return a.pid
}

// Wait returns once the process has ended, with StatusUnknown and an
// ExitValue whose Code is -1: its status is a parent's to read.
func (a *adopted) Wait() (exit coreproc.ExitValue, err error) {
	a.watch()
	<-a.done
	//: the end, without a status.
	return coreproc.ExitValue{Code: unknownExitCode}, wrapStatusUnknown(a.pid)
}

// WaitContext waits like Wait until the process ends or ctx is done; when ctx
// ends first it returns ctx's error, and a later wait still observes the end.
func (a *adopted) WaitContext(ctx context.Context) (exit coreproc.ExitValue, err error) {
	a.watch()
	select {
	//: the end.
	case <-a.done:
		//: without a status.
		return coreproc.ExitValue{Code: unknownExitCode}, wrapStatusUnknown(a.pid)
	//: the caller stopped waiting.
	case <-ctx.Done():
		//: the context's error; the watcher keeps watching.
		return coreproc.ExitValue{}, ctx.Err()
	}
}

// Signal delivers sig to the process held, never to one that reused its pid
// (deliver).
func (a *adopted) Signal(sig coreproc.Signal) error {
	//: the platform's guarded delivery.
	return a.deliver(sig)
}

// SignalGroup delivers sig to the process alone: a group addressed by number
// is not race-free, and this handle holds no group.
func (a *adopted) SignalGroup(sig coreproc.Signal) error {
	//: the process, as Signal reaches it.
	return a.deliver(sig)
}

// Stop sends sig, waits for the end up to grace, then sends SIGKILL and waits
// until the end or ctx's. A process already gone is success at either phase.
func (a *adopted) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	a.watch()
	//: phase one: the graceful request.
	if err := a.signalAllowGone(sig); err != nil {
		//: a real delivery fault.
		return err
	}
	//: a positive grace is a window for the end.
	if grace > 0 {
		ended, err := a.awaitGrace(ctx, grace)
		//: gone, or the caller stopped waiting.
		if ended || err != nil {
			//: no escalation.
			return err
		}
	}
	//: phase two: the unignorable kill.
	if err := a.signalAllowGone(coreproc.Signal(syscall.SIGKILL)); err != nil {
		//: the process could not be stopped.
		return wrapStop(err, errs.Int("pid", a.pid))
	}
	select {
	//: gone.
	case <-a.done:
		//: stopped.
		return nil
	//: the caller stopped waiting.
	case <-ctx.Done():
		//: its error.
		return ctx.Err()
	}
}

// awaitGrace waits for the end up to grace on the handle's clock.
func (a *adopted) awaitGrace(ctx context.Context, grace time.Duration) (ended bool, err error) {
	timer := a.clk.NewTimer(grace)
	defer timer.Stop()
	select {
	//: gone within the grace.
	case <-a.done:
		//: no escalation.
		return true, nil
	//: the caller stopped waiting.
	case <-ctx.Done():
		//: its error.
		return false, ctx.Err()
	//: the grace closed with the process alive.
	case <-timer.C():
		//: escalate.
		return false, nil
	}
}

// signalAllowGone delivers sig and treats ESRCH as success.
func (a *adopted) signalAllowGone(sig coreproc.Signal) error {
	err := a.deliver(sig)
	//: a process already gone needs no signal.
	if err != nil && processGone(err) {
		//: success.
		return nil
	}
	//: delivered, or a real fault.
	return err
}

// watch starts, once, the goroutine that closes done at the end.
func (a *adopted) watch() {
	//: the first wait starts it; every other finds it running.
	a.watching.Do(a.startWatcher)
}
