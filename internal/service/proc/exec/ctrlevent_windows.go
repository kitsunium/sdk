//go:build windows

package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ctrlBreakEvent is CTRL_BREAK_EVENT (wincon.h), the one console control
// event a group receives.
const ctrlBreakEvent uintptr = 1

// procGenerateConsoleCtrlEvent sends a console control event to a process
// group (kernel32), bound lazily like the Job Object entry points.
var procGenerateConsoleCtrlEvent = modKernel32.NewProc("GenerateConsoleCtrlEvent")

// consoleEvent maps a Unix signal onto the console control event a console
// process group receives in its place (#933): SIGINT, SIGTERM, SIGHUP and
// SIGQUIT are all CTRL_BREAK_EVENT. CTRL_C_EVENT is never sent: Windows does
// not deliver it to one group — GenerateConsoleCtrlEvent succeeds and the
// group's processes do not receive it — and a group CREATE_NEW_PROCESS_GROUP
// made starts with Ctrl-C ignored. A Go child reads CTRL_BREAK as
// os.Interrupt, the SIGINT a Unix child would have read. The mapping is the
// signal package's Relay's. ok is false for a signal with no console analogue,
// SIGKILL among them: that one is TerminateProcess.
func consoleEvent(sig coreproc.Signal) (event uintptr, ok bool) {
	//: the four signals a console process can be asked to honour.
	switch syscall.Signal(sig.Int()) {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT:
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
