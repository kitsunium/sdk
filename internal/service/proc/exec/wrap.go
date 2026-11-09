package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sysexits exit-code constants mirrored from internal/core/proc/codes.go so the
// wrapped Errors carry the same status as their bare-sentinel siblings.
const (
	exitUsage       int = 64 // EX_USAGE — the caller's Spec is not usable as written.
	exitDataErr     int = 65 // EX_DATAERR — the input (a PID file) is malformed.
	exitNoUser      int = 67 // EX_NOUSER — a named user/group did not resolve.
	exitUnavailable int = 69 // EX_UNAVAILABLE — a required facility is absent.
	exitOSErr       int = 71 // EX_OSERR — an OS-level operation failed.
	exitIOErr       int = 74 // EX_IOERR — an I/O error occurred (capture writer failed).
	exitNoPerm      int = 77 // EX_NOPERM — the process may not be adopted.
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

// wrapCgroupUnavailable wraps a stat/validation cause onto the central
// CgroupUnavailable sentinel — the pre-spawn check that Spec.CgroupPath names an
// existing, delegated cgroup v2 directory.
func wrapCgroupUnavailable(cause error, fields ...errs.FieldValue) error {
	//: restate the CGROUP_UNAVAILABLE sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupUnavailable,
		Reason:   "CGROUP_UNAVAILABLE",
		Public:   "cgroup v2 is not available or not delegated",
		Private:  "service/proc/exec.Start: Spec.CgroupPath is missing, not a cgroup v2 directory, or not delegated",
		ExitCode: exitUnavailable,
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

// wrapStdioDrainTimeout builds the central StdioDrainTimeout error a Wait
// returns beside the real exit when the drain grace cut the capture pipes. It
// has no cause: nothing failed, something still held the pipes.
func wrapStdioDrainTimeout(fields ...errs.FieldValue) error {
	//: restate the STDIO_DRAIN_TIMEOUT sentinel fields on a cause-less error.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeStdioDrainTimeout,
		Reason:   "STDIO_DRAIN_TIMEOUT",
		Public:   "The process exited but its output was still held open",
		Private:  "service/proc/exec.Wait: the capture pipes were still held open when the drain grace closed after the child's exit",
		ExitCode: exitIOErr,
	}, fields...)
}

// wrapHandleUnavailable wraps the reason no kernel handle could be had for a
// process onto the central HandleUnavailable sentinel.
func wrapHandleUnavailable(cause error, fields ...errs.FieldValue) error {
	//: restate the HANDLE_UNAVAILABLE sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeHandleUnavailable,
		Reason:   "HANDLE_UNAVAILABLE",
		Public:   "No process handle is available",
		Private:  "service/proc/exec: no pidfd or process HANDLE could be obtained for the process",
		ExitCode: exitUnavailable,
	}, fields...)
}

// wrapPTYFailed wraps a pseudo-terminal fault onto the central PTYFailed
// sentinel.
func wrapPTYFailed(cause error, fields ...errs.FieldValue) error {
	//: restate the PTY_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodePTYFailed,
		Reason:   "PTY_FAILED",
		Public:   "Could not set up the pseudo-terminal",
		Private:  "service/proc/exec: allocating, opening or resizing the pseudo-terminal failed",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapReservedEnv is the central ReservedEnv sentinel naming the Spec.Env key
// that belongs to the spawn protocol.
func wrapReservedEnv(key string) error {
	//: restate the RESERVED_ENV sentinel fields; there is no cause, only the key.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeReservedEnv,
		Reason:   "RESERVED_ENV",
		Public:   "The environment names a variable reserved by the SDK",
		Private:  "service/proc/exec.Start: Spec.Env names a reserved __KITSUNIUM_SDK_PROC_ variable",
		ExitCode: exitUsage,
	}, errs.String("key", key))
}

// wrapPIDFileRefused wraps the reason a PID file cannot be read safely onto
// the central PIDFileRefused sentinel.
func wrapPIDFileRefused(cause error, fields ...errs.FieldValue) error {
	//: restate the PID_FILE_REFUSED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodePIDFileRefused,
		Reason:   "PID_FILE_REFUSED",
		Public:   "The PID file cannot be read safely",
		Private:  "service/proc/exec.AttachPIDFile: the PID file is not a regular file reached safely, or holds no single PID; the why field says which",
		ExitCode: exitDataErr,
	}, fields...)
}

// wrapAdoptionRefused wraps the reason a process may not be adopted onto the
// central AdoptionRefused sentinel.
func wrapAdoptionRefused(cause error, fields ...errs.FieldValue) error {
	//: restate the ADOPTION_REFUSED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeAdoptionRefused,
		Reason:   "ADOPTION_REFUSED",
		Public:   "The process may not be adopted",
		Private:  "service/proc/exec.Attach: the process is PID 1, this process, outside the cgroup or users asked for, or named by an untrusted PID file with no bound",
		ExitCode: exitNoPerm,
	}, fields...)
}

// wrapStatusUnknown is the central StatusUnknown sentinel an attached
// process's Wait returns once it has ended.
func wrapStatusUnknown(pid int) error {
	//: restate the STATUS_UNKNOWN sentinel fields; no cause, only the pid.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    coreproc.CodeStatusUnknown,
		Reason:  "STATUS_UNKNOWN",
		Public:  "The process ended with an unknown status",
		Private: "service/proc/exec: an attached process is not this process's child; it ended and its exit status cannot be read",
	}, errs.Int("pid", pid))
}

// wrapInvalidSpec is the central InvalidSpec sentinel with fields naming the
// malformed part of the Spec.
func wrapInvalidSpec(fields ...errs.FieldValue) error {
	//: restate the INVALID_SPEC sentinel fields; no cause, only the fields.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeInvalidSpec,
		Reason:   "INVALID_SPEC",
		Public:   "Process specification is invalid",
		Private:  "service/proc/exec.Start: Spec is malformed (empty Path or contradictory attributes)",
		ExitCode: exitUsage,
	}, fields...)
}

// wrapSandboxFailed wraps the errno of a sandbox step that failed in the
// child onto the central SandboxFailed sentinel, naming the step.
func wrapSandboxFailed(cause error, fields ...errs.FieldValue) error {
	//: restate the SANDBOX_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSandboxFailed,
		Reason:   "SANDBOX_FAILED",
		Public:   "Could not confine the process",
		Private:  "service/proc/exec.Start: a step of Spec.Sandbox failed in the child before its program ran; the step field names it",
		ExitCode: exitOSErr,
	}, fields...)
}

// wrapSandboxUnavailable wraps the reason this kernel cannot apply a part of
// Spec.Sandbox onto the central SandboxUnavailable sentinel, naming it.
func wrapSandboxUnavailable(cause error, fields ...errs.FieldValue) error {
	//: restate the SANDBOX_UNAVAILABLE sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSandboxUnavailable,
		Reason:   "SANDBOX_UNAVAILABLE",
		Public:   "The kernel cannot apply the requested confinement",
		Private:  "service/proc/exec.Start: this kernel lacks a part of Spec.Sandbox; the feature field names it",
		ExitCode: exitUnavailable,
	}, fields...)
}

// wrapUnsupportedPlatform is the central UnsupportedPlatform sentinel with
// fields naming what this platform lacks.
func wrapUnsupportedPlatform(fields ...errs.FieldValue) error {
	//: restate the UNSUPPORTED_PLATFORM sentinel fields; no cause, only the fields.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeUnsupportedPlatform,
		Reason:   "UNSUPPORTED_PLATFORM",
		Public:   "This operation is not supported on this platform",
		Private:  "core/proc: primitive unavailable on the current GOOS; the stub returns this instead of acting",
		ExitCode: exitUnavailable,
	}, fields...)
}
