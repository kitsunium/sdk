//go:build windows

package exec

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Process access rights Open asks for (winnt.h): terminate it, wait on it,
// read its exit code.
const (
	processTerminate               uint32 = 0x0001
	processSynchronize             uint32 = 0x00100000
	processQueryLimitedInformation uint32 = 0x1000
	waitObject0                    uint32 = 0x00000000
	waitTimedOut                   uint32 = 0x00000102
	waitSlice                      uint32 = 50
	terminatedExitCode             uint32 = 1
)

// errorInvalidParameter is ERROR_INVALID_PARAMETER (winerror.h), OpenProcess's
// answer for a pid that names no process; syscall does not export it.
const errorInvalidParameter syscall.Errno = 87

// foreign is the handle Open returns on Windows: a process HANDLE opened with
// the rights to terminate and wait on the process, closed when the handle is
// collected. A HANDLE keeps its process object alive, so its pid cannot be
// reused while it is held.
type foreign struct {
	handle syscall.Handle
	pid    int
}

// openProcess opens pid with PROCESS_TERMINATE | SYNCHRONIZE |
// PROCESS_QUERY_LIMITED_INFORMATION. A pid that names no process is
// SIGNAL_FAILED wrapping ESRCH; a refusal (another session's process) is
// HANDLE_UNAVAILABLE.
func openProcess(pid int) (coreproc.Process, error) {
	h, oErr := syscall.OpenProcess(processTerminate|processSynchronize|processQueryLimitedInformation, false, uint32(pid))
	//: classify the refusal.
	if oErr != nil {
		//: ERROR_INVALID_PARAMETER is OpenProcess's word for no such process.
		if errors.Is(oErr, errorInvalidParameter) {
			//: the process is gone.
			return nil, wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
		}
		//: a process exists but is not ours to hold.
		return nil, wrapHandleUnavailable(oErr, errs.Int("pid", pid))
	}
	f := &foreign{handle: h, pid: pid}
	//: the HANDLE lives as long as the value that holds it.
	runtime.AddCleanup(f, func(h syscall.Handle) { swallowErr(syscall.CloseHandle(h)) }, h)
	//: a HANDLE on that process and no other.
	return f, nil
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

// Signal terminates the process through its HANDLE for SIGKILL; Windows has
// no other signal to deliver to a process it did not start in a console group.
func (f *foreign) Signal(sig coreproc.Signal) error {
	//: the one signal path of an opened process.
	return f.SignalHandle(sig)
}

// SignalGroup reaches the process alone.
func (f *foreign) SignalGroup(sig coreproc.Signal) error {
	//: the process, through its HANDLE.
	return f.SignalHandle(sig)
}

// SignalHandle terminates the process through its HANDLE for SIGKILL and
// refuses every other signal with SIGNAL_FAILED, as os.Process.Signal does.
func (f *foreign) SignalHandle(sig coreproc.Signal) error {
	defer runtime.KeepAlive(f)
	//: only a kill has a Windows meaning here.
	if sig != coreproc.Signal(syscall.SIGKILL) {
		//: the runtime's own refusal for a non-kill signal.
		return wrapSignal(syscall.EWINDOWS, errs.Int("pid", f.pid), errs.Int("signal", sig.Int()))
	}
	//: TerminateProcess on the HANDLE, never a pid reopened.
	if err := syscall.TerminateProcess(f.handle, terminatedExitCode); err != nil {
		//: the kernel's refusal.
		return wrapSignal(err, errs.Int("pid", f.pid), errs.Int("signal", sig.Int()))
	}
	//: the process is terminating.
	return nil
}

// Handle returns a duplicate of the process HANDLE, owned by the caller.
func (f *foreign) Handle() (*os.File, bool) {
	defer runtime.KeepAlive(f)
	//: the caller's own HANDLE on the same process.
	return duplicateProcessHandle(uintptr(f.handle))
}

// Stop sends sig (a non-kill one is refused by Windows and ignored), waits up
// to grace for the exit, then terminates the process and waits for it.
func (f *foreign) Stop(ctx context.Context, grace time.Duration, sig coreproc.Signal) error {
	//: best-effort graceful nudge; Windows refuses a non-kill signal.
	swallowErr(f.SignalHandle(sig))
	//: a positive grace is a window for the exit.
	if grace > 0 {
		exited, err := f.awaitExit(ctx, time.Now().Add(grace))
		//: gone, or the caller stopped waiting.
		if exited || err != nil {
			//: no escalation.
			return err
		}
	}
	//: the forced terminate, through the HANDLE; a process the first kill
	//: already ended answers ACCESS_DENIED here, and is simply gone.
	if err := f.SignalHandle(coreproc.Signal(syscall.SIGKILL)); err != nil {
		event, wErr := syscall.WaitForSingleObject(f.handle, 0)
		//: not ended after all: the process could not be stopped.
		if wErr != nil || event != waitObject0 {
			return wrapStop(err, errs.Int("pid", f.pid))
		}
	}
	_, err := f.awaitExit(ctx, time.Time{})
	//: nil once gone, ctx's error otherwise.
	return err
}

// awaitExit waits on the HANDLE one slice at a time until the process exits,
// ctx ends, or deadline (zero: none) passes.
func (f *foreign) awaitExit(ctx context.Context, deadline time.Time) (exited bool, err error) {
	defer runtime.KeepAlive(f)
	//: one WaitForSingleObject slice per turn.
	for {
		//: the caller stopped waiting.
		if ctxErr := ctx.Err(); ctxErr != nil {
			//: ctx's error.
			return false, ctxErr
		}
		//: the grace closed with the process alive.
		if !deadline.IsZero() && time.Now().After(deadline) {
			//: not settled.
			return false, nil
		}
		event, wErr := syscall.WaitForSingleObject(f.handle, waitSlice)
		//: classify the slice.
		switch {
		case wErr != nil:
			//: the wait itself failed.
			return false, wrapStop(wErr, errs.Int("pid", f.pid))
		case event == waitObject0:
			//: the process object is signalled: it has exited.
			return true, nil
		case event != waitTimedOut:
			//: an answer WaitForSingleObject does not give a process.
			return false, wrapStop(syscall.EINVAL, errs.Int("pid", f.pid))
		}
	}
}
