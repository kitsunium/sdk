package signal

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sendValueExitCode is sysexits.h EX_OSERR (71), the exit status the proc
// domain gives an OS-level operation failure.
const sendValueExitCode int = 71

// SendValue queues sig with value to the process target — rt_sigqueueinfo(2)
// with SI_QUEUE on Linux, sigqueue(2) on FreeBSD — where kill(2) would deliver
// the signal alone. A realtime signal queues instead of coalescing. The value
// reaches a C handler's si_value; a Go receiver cannot read it (os/signal
// exposes no siginfo). target names one process: a group, 0 and -1 are refused.
// It returns SIGNAL_FAILED wrapping the errno — ESRCH for a reaped pid — and
// UNSUPPORTED_PLATFORM elsewhere.
func SendValue(target Target, sig coreproc.Signal, value int) error {
	//: a queued signal names one process: a group, 0 and -1 are refused.
	if target <= 0 {
		//: the kernel would read them as another call entirely.
		return sendFailed(syscall.EINVAL, target, sig)
	}
	//: the platform's value-carrying send.
	return sendValue(target, sig, value)
}

// sendFailed wraps a refused or failed queued send under SIGNAL_FAILED.
func sendFailed(cause error, target Target, sig coreproc.Signal) error {
	//: the errno stays the cause, so ESRCH reads as ESRCH.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSignalFailed,
		Reason:   "SIGNAL_FAILED",
		Public:   "Could not deliver the signal to the process",
		Private:  "service/proc/signal.SendValue: queueing the signal with its value failed",
		ExitCode: sendValueExitCode,
	}, errs.Int("target", int(target)), errs.Int("signal", sig.Int()))
}
