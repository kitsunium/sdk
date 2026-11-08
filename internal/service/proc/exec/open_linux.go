//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sysPidfdOpen is __NR_pidfd_open, 434 on every Linux architecture of the
// unified syscall table (Linux 5.3).
const sysPidfdOpen uintptr = 434

// foreign is the handle Open returns on Linux: a process this one did not
// spawn, held through the pidfd os.FindProcess opened (pidfd_open). Nothing in
// it addresses the pid by number, so a pid reused after the process exits is
// never reached. The runtime closes the pidfd when the handle is collected.
type foreign struct {
	proc *os.Process
	pid  int
	// clk is what Stop's grace window runs on: clock.System from Open.
	clk clock.Waiter
}

// openProcess opens pid through pidfd_open. A pid already gone is SIGNAL_FAILED
// wrapping ESRCH; a kernel that gave no pidfd is HANDLE_UNAVAILABLE — the
// runtime then holds only the number, which is the race Open exists to avoid.
func openProcess(pid int) (coreproc.Process, error) {
	p, fErr := os.FindProcess(pid)
	//: FindProcess never fails on Unix; a failure here is the kernel's word.
	if fErr != nil {
		//: the cause under the central SIGNAL_FAILED fields.
		return nil, wrapSignal(fErr, errs.Int("pid", pid))
	}
	//: the runtime holds a pidfd on that process and no other.
	if p.WithHandle(func(uintptr) {}) == nil {
		//: the race-free handle.
		return &foreign{proc: p, pid: pid, clk: clock.System}, nil
	}
	//: FindProcess hides why it holds none — a process already gone, or a
	//: kernel without pidfd — so pidfd_open is asked once more for the reason.
	return nil, whyNoPidfd(pid)
}

// whyNoPidfd classifies a pid os.FindProcess holds no pidfd for: ESRCH from
// pidfd_open is a process gone (SIGNAL_FAILED), anything else — ENOSYS
// before Linux 5.3, a filter's EPERM — is HANDLE_UNAVAILABLE.
func whyNoPidfd(pid int) error {
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	//: a pidfd after all: the runtime's own probe refused pidfd, so is this one.
	if errno == 0 {
		swallowErr(syscall.Close(int(fd)))
		//: the runtime would not use it either.
		return wrapHandleUnavailable(os.ErrNoHandle, errs.Int("pid", pid))
	}
	//: no such process.
	if errno == syscall.ESRCH {
		//: the verdict a signal through a pidfd would give.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: no pidfd on this kernel.
	return wrapHandleUnavailable(errno, errs.Int("pid", pid))
}

// PID reports the process identifier given to Open.
func (f *foreign) PID() int {
	//: the number the handle was opened from.
	return f.pid
}

// Wait is refused: only a parent collects an exit status.
func (f *foreign) Wait() (exit coreproc.ExitValue, err error) {
	//: WAIT_FAILED wrapping ECHILD.
	return coreproc.ExitValue{}, wrapForeignWait(f.pid)
}

// WaitContext is refused like Wait.
func (f *foreign) WaitContext(_ context.Context) (exit coreproc.ExitValue, err error) {
	//: WAIT_FAILED wrapping ECHILD.
	return coreproc.ExitValue{}, wrapForeignWait(f.pid)
}

// Signal delivers sig through the pidfd.
func (f *foreign) Signal(sig coreproc.Signal) error {
	//: the race-free path is the only path.
	return f.SignalHandle(sig)
}

// SignalGroup delivers sig to the process alone: a group addressed by number
// is not race-free, and this handle holds no group.
func (f *foreign) SignalGroup(sig coreproc.Signal) error {
	//: the process, through its pidfd.
	return f.SignalHandle(sig)
}

// SignalHandle delivers sig through the pidfd (pidfd_send_signal); once the
// process has exited it fails with SIGNAL_FAILED wrapping ESRCH.
func (f *foreign) SignalHandle(sig coreproc.Signal) error {
	//: the one signal path of an opened process.
	return signalThrough(f.proc, f.pid, sig)
}

// Handle returns a duplicate of the pidfd, owned by the caller.
func (f *foreign) Handle() (*os.File, bool) {
	//: the caller's own descriptor for the same process.
	return dupProcessHandle(f.proc)
}

// Stop sends sig through the pidfd, watches the pidfd for the exit up to
// grace, then sends SIGKILL and watches until the exit or ctx's end. A process
// already gone is success at either phase.
func (f *foreign) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	//: phase one: the graceful request.
	if err := f.signalAllowGone(sig); err != nil {
		//: a real delivery fault.
		return err
	}
	//: a positive grace is a window for the exit.
	if grace > 0 {
		exited, err := f.awaitExit(ctx, f.clk.NewTimer(grace).C())
		//: gone, or the caller stopped waiting.
		if exited || err != nil {
			//: no escalation.
			return err
		}
	}
	//: phase two: the unignorable kill.
	if err := f.signalAllowGone(coreproc.Signal(syscall.SIGKILL)); err != nil {
		//: the process could not be stopped.
		return wrapStop(err, errs.Int("pid", f.pid))
	}
	_, err := f.awaitExit(ctx, nil)
	//: nil once gone, ctx's error otherwise.
	return err
}

// signalAllowGone signals through the pidfd and treats ESRCH as success.
func (f *foreign) signalAllowGone(sig coreproc.Signal) error {
	err := f.SignalHandle(sig)
	//: a process already gone needs no signal.
	if err != nil && processGone(err) {
		//: success.
		return nil
	}
	//: delivered, or a real fault.
	return err
}

// awaitExit polls the pidfd one slice at a time until it reports the exit,
// ctx ends, or deadline (nil: none) fires. exited reports the first.
func (f *foreign) awaitExit(ctx context.Context, deadline <-chan time.Time) (exited bool, err error) {
	//: one ppoll slice per turn, between which ctx and the deadline are read.
	for {
		select {
		case <-ctx.Done():
			//: the caller stopped waiting.
			return false, ctx.Err()
		case <-deadline:
			//: the grace closed with the process alive.
			return false, nil
		default:
		}
		var gone bool
		var pErr error
		hErr := f.proc.WithHandle(func(fd uintptr) {
			//: one slice of the wait on the pidfd.
			gone, pErr = pidfdExited(fd, true)
		})
		//: a poll fault or a lost handle ends the watch.
		if err := errors.Join(hErr, pErr); err != nil {
			//: the watch cannot continue.
			return false, wrapStop(err, errs.Int("pid", f.pid))
		}
		//: the pidfd turned readable: the process has exited.
		if gone {
			//: success.
			return true, nil
		}
	}
}
