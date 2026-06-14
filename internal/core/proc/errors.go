// Package proc — declares the sentinels returned across the process-supervision
// domain. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form;
// service implementations and pkg/v1 facades wrap these, never re-Define them.
package proc

import "github.com/kitsunium/sdk/internal/kernel/errs"

// sysexits exit-code mapping (see sysexits.h) — chosen so a supervisor that
// surfaces err.ExitCode() reports a meaningful status rather than a generic 70.
const (
	exitUsage       int = 64 // EX_USAGE — caller passed an invalid argument.
	exitDataErr     int = 65 // EX_DATAERR — input data was malformed.
	exitNoUser      int = 67 // EX_NOUSER — a named user/group did not resolve.
	exitUnavailable int = 69 // EX_UNAVAILABLE — a required facility is absent.
	exitOSErr       int = 71 // EX_OSERR — an OS-level operation failed.
	exitIOErr       int = 74 // EX_IOERR — an I/O error occurred.
	exitNoPerm      int = 77 // EX_NOPERM — a permission/credential check failed.
)

var (
	// UnsupportedPlatform is returned by a primitive invoked on an operating
	// system that cannot provide it; the no-op stub returns it, never a panic.
	UnsupportedPlatform = errs.Define(CodeUnsupportedPlatform, "UNSUPPORTED_PLATFORM",
		"This operation is not supported on this platform",
		"core/proc: primitive unavailable on the current GOOS; the stub returns this instead of acting",
		errs.WithExitCode(exitUnavailable))

	// InvalidSpec is returned by Start when the Spec is malformed — an empty Path
	// or a contradictory attribute combination.
	InvalidSpec = errs.Define(CodeInvalidSpec, "INVALID_SPEC",
		"Process specification is invalid",
		"service/proc/exec.Start: Spec is malformed (empty Path or contradictory attributes)",
		errs.WithExitCode(exitUsage))

	// UnknownUser is returned when a Spec names a User that does not resolve to a
	// uid on the host.
	UnknownUser = errs.Define(CodeUnknownUser, "UNKNOWN_USER",
		"The requested user does not exist",
		"service/proc/exec.Start: Spec.User did not resolve to a uid via os/user",
		errs.WithExitCode(exitNoUser))

	// UnknownGroup is returned when a Spec names a Group or supplementary group
	// that does not resolve to a gid on the host.
	UnknownGroup = errs.Define(CodeUnknownGroup, "UNKNOWN_GROUP",
		"The requested group does not exist",
		"service/proc/exec.Start: Spec.Group/Groups did not resolve to a gid via os/user",
		errs.WithExitCode(exitNoUser))

	// SpawnFailed is returned when fork/exec fails — a missing binary, a
	// permission denial, or a kernel resource fault.
	SpawnFailed = errs.Define(CodeSpawnFailed, "SPAWN_FAILED",
		"Could not start the process",
		"service/proc/exec.Start: fork/exec failed",
		errs.WithExitCode(exitOSErr))

	// WaitFailed is returned when wait4 fails reaping the leader — a host fault,
	// not a normal non-zero exit (reported via ExitValue instead).
	WaitFailed = errs.Define(CodeWaitFailed, "WAIT_FAILED",
		"Could not wait for the process to exit",
		"service/proc/exec.Wait: wait4 returned an unexpected error",
		errs.WithExitCode(exitOSErr))

	// SignalFailed is returned when kill(2) fails delivering a signal to a
	// process or process group.
	SignalFailed = errs.Define(CodeSignalFailed, "SIGNAL_FAILED",
		"Could not deliver the signal",
		"service/proc: kill(2) failed for the target pid or process group",
		errs.WithExitCode(exitOSErr))

	// StopFailed is returned when a graceful stop cannot terminate the process
	// group even after escalating to SIGKILL.
	StopFailed = errs.Define(CodeStopFailed, "STOP_FAILED",
		"Could not stop the process",
		"service/proc/exec.Stop: process group survived signal escalation to SIGKILL",
		errs.WithExitCode(exitOSErr))

	// UnknownSignal is returned by Parse when the name or number is not in the
	// platform signal table.
	UnknownSignal = errs.Define(CodeUnknownSignal, "UNKNOWN_SIGNAL",
		"Unknown signal name or number",
		"core/proc.Parse: input is not a signal known on this platform",
		errs.WithExitCode(exitUsage))

	// RelayFailed is returned by Relay when a received signal cannot be forwarded
	// to its target process or group.
	RelayFailed = errs.Define(CodeRelayFailed, "RELAY_FAILED",
		"Could not relay the signal to the target",
		"service/proc/signal.Relay: forwarding the signal to the target failed",
		errs.WithExitCode(exitOSErr))

	// SubreaperFailed is returned when prctl(PR_SET_CHILD_SUBREAPER) fails arming
	// subreaper semantics for a non-PID1 supervisor.
	SubreaperFailed = errs.Define(CodeSubreaperFailed, "SUBREAPER_FAILED",
		"Could not enable child-subreaper mode",
		"service/proc/reaper.SetChildSubreaper: prctl(PR_SET_CHILD_SUBREAPER) failed",
		errs.WithExitCode(exitOSErr))

	// ReapFailed is returned when a reap sweep hits a wait4 error other than the
	// benign "no children" condition.
	ReapFailed = errs.Define(CodeReapFailed, "REAP_FAILED",
		"Could not reap child processes",
		"service/proc/reaper.ReapOnce: wait4 failed with an error other than ECHILD",
		errs.WithExitCode(exitOSErr))

	// UnknownResource is returned by Apply when a Resource has no RLIMIT_*
	// mapping on the platform.
	UnknownResource = errs.Define(CodeUnknownResource, "UNKNOWN_RESOURCE",
		"Unknown or unsupported resource limit",
		"service/proc/rlimit.Apply: Resource has no RLIMIT_* mapping on this platform",
		errs.WithExitCode(exitUsage))

	// RlimitFailed is returned when setrlimit(2) fails applying a per-process
	// resource limit.
	RlimitFailed = errs.Define(CodeRlimitFailed, "RLIMIT_FAILED",
		"Could not apply the resource limit",
		"service/proc/rlimit.Apply: setrlimit(2) failed",
		errs.WithExitCode(exitOSErr))

	// CgroupUnavailable is returned when a cgroup operation runs where cgroup v2
	// is not mounted or not delegated to the caller.
	CgroupUnavailable = errs.Define(CodeCgroupUnavailable, "CGROUP_UNAVAILABLE",
		"cgroup v2 is not available or not delegated",
		"service/proc/cgroup: the unified cgroup v2 hierarchy is absent or not writable by the caller",
		errs.WithExitCode(exitUnavailable))

	// CgroupCreateFailed is returned when a control-group directory cannot be
	// created under the cgroup v2 hierarchy.
	CgroupCreateFailed = errs.Define(CodeCgroupCreateFailed, "CGROUP_CREATE_FAILED",
		"Could not create the control group",
		"service/proc/cgroup.Create: mkdir under the cgroup v2 hierarchy failed",
		errs.WithExitCode(exitOSErr))

	// CgroupWriteFailed is returned when a controller file cannot be written
	// (memory.max, cpu.max, pids.max, io.max, cgroup.procs).
	CgroupWriteFailed = errs.Define(CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
		"Could not write the cgroup controller file",
		"service/proc/cgroup: writing a controller file failed (controller disabled or value rejected)",
		errs.WithExitCode(exitOSErr))

	// CgroupDeleteFailed is returned when a control-group directory cannot be
	// removed.
	CgroupDeleteFailed = errs.Define(CodeCgroupDeleteFailed, "CGROUP_DELETE_FAILED",
		"Could not delete the control group",
		"service/proc/cgroup.Delete: rmdir of the control group failed (still populated?)",
		errs.WithExitCode(exitOSErr))

	// NotifyFailed is returned when an sd_notify datagram cannot be sent to
	// $NOTIFY_SOCKET — distinct from the no-op when the variable is unset.
	NotifyFailed = errs.Define(CodeNotifyFailed, "NOTIFY_FAILED",
		"Could not send the sd_notify datagram",
		"service/proc/sdnotify: writing to $NOTIFY_SOCKET failed",
		errs.WithExitCode(exitOSErr))

	// ListenFailed is returned when the supervisor-side sd_notify datagram socket
	// cannot be created or bound.
	ListenFailed = errs.Define(CodeListenFailed, "LISTEN_FAILED",
		"Could not create the sd_notify listener socket",
		"service/proc/sdnotify.Listen: creating or binding the AF_UNIX datagram socket failed",
		errs.WithExitCode(exitOSErr))

	// InvalidNotification is returned when a received sd_notify datagram cannot
	// be parsed into NAME=value fields.
	InvalidNotification = errs.Define(CodeInvalidNotification, "INVALID_NOTIFICATION",
		"Received sd_notify datagram is malformed",
		"service/proc/sdnotify.Recv: datagram did not parse into NAME=value fields",
		errs.WithExitCode(exitDataErr))

	// CredentialMismatch is returned when a received datagram's kernel-verified
	// sender credentials do not match the expected supervised process.
	CredentialMismatch = errs.Define(CodeCredentialMismatch, "CREDENTIAL_MISMATCH",
		"Datagram sender credentials did not match",
		"service/proc/sdnotify.Recv: SO_PASSCRED sender pid is not the expected supervised process",
		errs.WithExitCode(exitNoPerm))

	// StdioCaptureFailed is returned from Wait when a StdioCapture copier could
	// not deliver the child's stdout/stderr to the caller's writer (the writer
	// itself errored), even though the process exited cleanly.
	StdioCaptureFailed = errs.Define(CodeStdioCaptureFailed, "STDIO_CAPTURE_FAILED",
		"Could not deliver the captured output to the writer",
		"service/proc/exec: a StdioCapture copier failed writing the child's stdout/stderr to the caller's sink",
		errs.WithExitCode(exitIOErr))
)
