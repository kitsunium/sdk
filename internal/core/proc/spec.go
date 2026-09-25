// Package proc — the Spec value type: an immutable process spawn specification.
package proc

import (
	"io"
	"os"
)

// Spec is the immutable description of a process to spawn: the executable and
// its environment, the credentials and isolation topology to apply, and the
// resource/scheduling attributes to set between fork and exec. It carries no
// policy — restart, health, and dependency concerns belong to the caller.
//
// Pointer fields (Umask, Nice, OOMScoreAdj) distinguish "leave at the inherited
// default" (nil) from "set explicitly to zero". Maps and slices are read by the
// service layer and never mutated.
type Spec struct {
	// Path is the executable to run. Required. A value containing a path
	// separator is a file path, taken as written (a relative one is resolved
	// against Dir). A bare name — "go", "sh" — is searched in the PATH the
	// CHILD will see: the PATH entry of Env when it carries one, the parent's
	// otherwise (including when Env is nil). os/exec's rules apply: the first
	// executable in PATH order wins, and a match found only through a relative
	// entry ("." or an empty one) is refused with exec.ErrDot.
	Path string
	// Args is the full argv including argv[0]; when empty the service uses
	// [Path] — the name as written, not the resolved file — as the sole
	// argument.
	Args []string
	// Dir is the working directory; empty means inherit the parent's.
	Dir string
	// Env is the explicit environment; nil yields an empty environment, never
	// implicit inheritance, so a spawned service starts from a known state.
	Env []string

	// User is the name or numeric uid to setuid to; empty leaves the parent's.
	User string
	// Group is the name or numeric gid to setgid to; empty leaves the parent's.
	Group string
	// Groups are supplementary group names or gids (systemd SupplementaryGroups=).
	Groups []string

	// Setpgid places the child in its own process group, enabling group-kill of
	// the whole tree (systemd KillMode=control-group without cgroups).
	Setpgid bool
	// Setsid starts the child in a new session detached from the controlling tty.
	Setsid bool

	// Umask, when non-nil, is the file-creation mask applied to the child
	// (systemd UMask=).
	Umask *int
	// Nice, when non-nil, is the scheduling-priority adjustment applied to the
	// child (systemd Nice=).
	Nice *int
	// OOMScoreAdj, when non-nil, is the /proc/<pid>/oom_score_adj value applied
	// to the child (systemd OOMScoreAdjust=).
	OOMScoreAdj *int
	// Rlimits are per-resource soft/hard limits applied post-fork, pre-exec
	// (systemd Limit*=); resources unsupported on the platform surface a typed
	// error rather than silently no-op.
	Rlimits map[Resource]LimitValue

	// CgroupPath, when non-empty, is an already-created cgroup v2 directory the
	// child is placed into BEFORE exec, so controller limits (MemoryMax/TasksMax/
	// …) apply from its first instruction — closing the unconfined window that a
	// post-spawn Group.Add(pid) leaves open (systemd places the unit's payload in
	// its cgroup the same way). The service writes the child's pid to
	// <CgroupPath>/cgroup.procs in the pre-exec trampoline. Linux-only: a non-empty
	// value on any other GOOS surfaces UnsupportedPlatform; a path that is missing,
	// not a cgroup v2 directory, or not delegated surfaces a typed cgroup error
	// rather than spawning unconfined.
	CgroupPath string

	// Stdio selects how the child's standard streams are wired: StdioInherit
	// (default — share the parent's), StdioNull (discard to the null device), or
	// StdioCapture (use the Stdout/Stderr/Stdin members below). systemd
	// StandardOutput=/StandardError=/StandardInput= is the analogue.
	Stdio StdioMode
	// Stdout, when Stdio is StdioCapture, receives the child's standard output;
	// a nil writer discards that stream to the null device.
	Stdout io.Writer
	// Stderr, when Stdio is StdioCapture, receives the child's standard error;
	// a nil writer discards that stream to the null device.
	Stderr io.Writer
	// Stdin, when Stdio is StdioCapture, is copied to the child's standard input;
	// a nil reader gives the child an immediate-EOF stdin. The reader should be
	// EOF-terminating (a buffer, file, or strings.Reader); the copier closes the
	// child's stdin at EOF and never blocks Wait.
	Stdin io.Reader

	// ExtraFiles are additional open files inherited by the child, in order,
	// starting at file descriptor 3 (after stdin/stdout/stderr). This is the
	// mechanism behind socket activation (sd_listen_fds): an activator binds
	// listening sockets and passes them here, then sets LISTEN_FDS/LISTEN_PID in
	// Env so the child finds them at fd 3..3+len-1. nil means no extra fds.
	ExtraFiles []*os.File
}
