//go:build windows

package signal

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR (71), the exit status RelayFailed carries.
const exitOSErr int = 71

// The console control event a group receives (wincon.h) and the OpenProcess
// terminate right (winnt.h).
const (
	ctrlBreakEvent   uintptr = 1
	processTerminate uintptr = 0x0001
)

// kernel32 delivery entry points, bound lazily.
var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGenerateConsoleCtrlEvent = modKernel32.NewProc("GenerateConsoleCtrlEvent")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procTerminateProcess         = modKernel32.NewProc("TerminateProcess")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")
)

// Relay reads signals from src and forwards each to target until src is closed.
// A positive target is a pid; a target below -1 addresses the process group
// whose id is -target. The reserved 0 and -1 are refused up front. The first
// delivery failure stops the relay with a RelayFailed error; a clean drain of
// src returns nil.
func Relay(src <-chan coreproc.Signal, target Target) error {
	//: refuse the reserved targets before delivering anything — 0 is the caller's
	//: own group and -1 every process it may signal; a mis-computed Target must
	//: never fan out that wide.
	if target == 0 || target == -1 {
		//: refuse with the central sentinel (origin wins on the *errs.Error cause).
		return relayFailed(coreproc.RelayFailed, target, 0)
	}
	//: forward each received signal until the source closes.
	for sig := range src {
		//: a delivery failure stops the relay and surfaces the typed error.
		if err := deliver(target, sig); err != nil {
			//: propagate the RelayFailed verbatim.
			return err
		}
	}
	//: the source drained cleanly with every signal delivered.
	return nil
}

// deliver routes one signal to its Windows primitive by target shape.
func deliver(target Target, sig coreproc.Signal) error {
	//: a group target uses a console control event; a pid uses TerminateProcess.
	if target < -1 {
		//: deliver to the whole console process group.
		return deliverGroup(target, sig)
	}
	//: deliver to the single process.
	return deliverPid(target, sig)
}

// deliverGroup sends CTRL_BREAK to the process group -target, whatever the
// signal, SIGINT included (#933): it is the only event Windows directs at one
// group. CTRL_C to a group is accepted by GenerateConsoleCtrlEvent and received
// by none of its processes, and a group created with CREATE_NEW_PROCESS_GROUP
// ignores Ctrl-C anyway; a Go process reads CTRL_BREAK as os.Interrupt.
func deliverGroup(target Target, sig coreproc.Signal) error {
	//: GenerateConsoleCtrlEvent(CTRL_BREAK, pgid) reaches every group member.
	r1, _, errno := procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(-int(target)))
	//: a zero return means the event could not be generated.
	if r1 == 0 {
		//: surface the Win32 cause through RELAY_FAILED.
		return relayFailed(errno, target, sig.Int())
	}
	//: the control event was delivered to the group.
	return nil
}

// deliverPid terminates the single process target. Windows has no per-pid
// interrupt, so any signal to a pid is the forceful TerminateProcess.
func deliverPid(target Target, sig coreproc.Signal) error {
	//: open the target with the terminate right only.
	h, _, errno := procOpenProcess.Call(processTerminate, 0, uintptr(int(target)))
	//: a zero handle means the process is gone or access was denied.
	if h == 0 {
		//: surface the open failure through RELAY_FAILED.
		return relayFailed(errno, target, sig.Int())
	}
	//: release the process handle once the terminate returns.
	defer procCloseHandle.Call(h)
	//: TerminateProcess(handle, exit=1) ends the target.
	r1, _, terrno := procTerminateProcess.Call(h, 1)
	//: a zero return means the terminate was refused.
	if r1 == 0 {
		//: surface the terminate failure through RELAY_FAILED.
		return relayFailed(terrno, target, sig.Int())
	}
	//: the process was terminated.
	return nil
}

// relayFailed wraps a cause in the central RELAY_FAILED sentinel, annotated with
// the target and signal. A nil/sentinel cause yields the bare RelayFailed
// (origin wins on wrap); a Win32 errno cause is wrapped under the restated fields.
func relayFailed(cause error, target Target, sig int) error {
	//: restate the central RELAY_FAILED fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRelayFailed,
		Reason:   "RELAY_FAILED",
		Public:   "Could not relay the signal to the target",
		Private:  "service/proc/signal.Relay: forwarding the signal to the target failed",
		ExitCode: exitOSErr,
	}, errs.Int("target", int(target)), errs.Int("signal", sig))
}
