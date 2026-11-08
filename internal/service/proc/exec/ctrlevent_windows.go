//go:build windows

package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// procGenerateConsoleCtrlEvent sends a console control event to a process
// group (kernel32), bound lazily like the Job Object entry points.
var procGenerateConsoleCtrlEvent = modKernel32.NewProc("GenerateConsoleCtrlEvent")

// Console control events (wincon.h).
const (
	ctrlCEvent     uintptr = 0
	ctrlBreakEvent uintptr = 1
)

// consoleEvent maps a Unix signal onto the console control event a console
// process group receives in its place (#933): SIGINT is CTRL_C_EVENT, and
// SIGTERM, SIGHUP and SIGQUIT are CTRL_BREAK_EVENT — the one event Windows
// delivers to a single group rather than to the whole console. The mapping is
// the signal package's Relay's. ok is false for a signal with no console
// analogue, SIGKILL among them: that one is TerminateProcess.
func consoleEvent(sig coreproc.Signal) (event uintptr, ok bool) {
	//: the four signals a console process can be asked to honour.
	switch syscall.Signal(sig.Int()) {
	case syscall.SIGINT:
		//: Ctrl-C. Windows does not limit it to one group: a group created with
		//: CREATE_NEW_PROCESS_GROUP starts with Ctrl-C ignored unless it
		//: re-enables it, so CTRL_BREAK is the event a group surely receives.
		return ctrlCEvent, true
	case syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT:
		//: Ctrl-Break, directed at one group.
		return ctrlBreakEvent, true
	default:
		//: no console analogue.
		return 0, false
	}
}

// sendConsoleEvent delivers event to the console process group led by pid —
// the group CREATE_NEW_PROCESS_GROUP gave a Setpgid child, whose id is its
// leader's pid. The caller must share the console: a supervisor without one
// is refused by the kernel, which is SIGNAL_FAILED.
func sendConsoleEvent(event uintptr, pid int, sig coreproc.Signal) error {
	r1, _, errno := procGenerateConsoleCtrlEvent.Call(event, uintptr(pid))
	//: a zero return is the kernel's refusal.
	if r1 == 0 {
		//: the central SIGNAL_FAILED, naming the group.
		return wrapSignal(errno, errs.Int("pgid", pid), errs.Int("signal", sig.Int()))
	}
	//: the group's console handlers run.
	return nil
}
