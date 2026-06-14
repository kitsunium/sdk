// Package exec — central wrap helpers. Every syscall/stdlib cause is wrapped
// back onto a coreproc sentinel by restating that sentinel's exact
// Reason/Public/Private/ExitCode here once, so call sites stay terse and the
// magic exit-code literals live in a single named place.
package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sysexits exit-code constants mirrored from internal/core/proc/codes.go so the
// wrapped Errors carry the same status as their bare-sentinel siblings.
const (
	exitNoUser int = 67 // EX_NOUSER — a named user/group did not resolve.
	exitOSErr  int = 71 // EX_OSERR — an OS-level operation failed.
	exitIOErr  int = 74 // EX_IOERR — an I/O error occurred (capture writer failed).
)

// wrapSpawn wraps a fork/exec cause onto the central SpawnFailed sentinel.
func wrapSpawn(cause error, fields ...errs.FieldValue) error {
	//: restate the SPAWN_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSpawnFailed,
		Reason:   "SPAWN_FAILED",
		Public:   "Could not start the process",
		Private:  "service/proc/exec.Start: fork/exec failed",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapWait wraps a wait4 host fault onto the central WaitFailed sentinel.
func wrapWait(cause error, fields ...errs.FieldValue) error {
	//: restate the WAIT_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeWaitFailed,
		Reason:   "WAIT_FAILED",
		Public:   "Could not wait for the process to exit",
		Private:  "service/proc/exec.Wait: wait4 returned an unexpected error",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapSignal wraps a kill(2) cause onto the central SignalFailed sentinel.
func wrapSignal(cause error, fields ...errs.FieldValue) error {
	//: restate the SIGNAL_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSignalFailed,
		Reason:   "SIGNAL_FAILED",
		Public:   "Could not deliver the signal",
		Private:  "service/proc: kill(2) failed for the target pid or process group",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapStop wraps a failed SIGKILL escalation onto the central StopFailed sentinel.
func wrapStop(cause error, fields ...errs.FieldValue) error {
	//: restate the STOP_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeStopFailed,
		Reason:   "STOP_FAILED",
		Public:   "Could not stop the process",
		Private:  "service/proc/exec.Stop: process group survived signal escalation to SIGKILL",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapRlimit wraps a refused scheduling/limit attribute onto the central
// RlimitFailed sentinel.
func wrapRlimit(cause error, fields ...errs.FieldValue) error {
	//: restate the RLIMIT_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not apply the resource limit",
		Private:  "service/proc/rlimit.Apply: setrlimit(2) failed",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapStdioCapture wraps a StdioCapture writer failure onto the central
// StdioCaptureFailed sentinel.
func wrapStdioCapture(cause error, fields ...errs.FieldValue) error {
	//: restate the STDIO_CAPTURE_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeStdioCaptureFailed,
		Reason:   "STDIO_CAPTURE_FAILED",
		Public:   "Could not deliver the captured output to the writer",
		Private:  "service/proc/exec.Wait: a StdioCapture copier failed writing the child's output to the caller's sink",
		ExitCode: exitIOErr,
	}, fields...)
}

// wrapUnknownUser wraps an os/user resolution cause onto the central UnknownUser
// sentinel.
func wrapUnknownUser(cause error, fields ...errs.FieldValue) error {
	//: restate the UNKNOWN_USER sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeUnknownUser,
		Reason:   "UNKNOWN_USER",
		Public:   "The requested user does not exist",
		Private:  "service/proc/exec.Start: Spec.User did not resolve to a uid via os/user",
		ExitCode: exitNoUser,
	}, fields...)
}

// wrapUnknownGroup wraps an os/user resolution cause onto the central
// UnknownGroup sentinel.
func wrapUnknownGroup(cause error, fields ...errs.FieldValue) error {
	//: restate the UNKNOWN_GROUP sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeUnknownGroup,
		Reason:   "UNKNOWN_GROUP",
		Public:   "The requested group does not exist",
		Private:  "service/proc/exec.Start: Spec.Group/Groups did not resolve to a gid via os/user",
		ExitCode: exitNoUser,
	}, fields...)
}
