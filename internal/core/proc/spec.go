package proc

import (
	"io"
	"os"
	"time"
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
	// CHILD will see: the PATH entry of Env whenever Env names one (an
	// explicitly empty PATH= searches nothing), the parent's only when Env
	// names none (including when Env is nil). os/exec's rules apply: the first
	// executable in PATH order wins, and a match found only through a relative
	// entry ("." or an empty one) is refused with exec.ErrDot.
	Path string
	// Args is the full argv including argv[0]; when empty the service uses
	// [Spec.Path] — the name as written, not the resolved file — as the sole
	// argument.
	Args []string
	// Dir is the working directory; empty means inherit the parent's.
	Dir string
	// Env is the explicit environment; nil yields an empty environment, never
	// implicit inheritance, so a spawned service starts from a known state. A key
	// set more than once keeps its last value, at the place of its last
	// occurrence, as os/exec reads append(os.Environ(), "K=v"). A key beginning
	// __KITSUNIUM_SDK_PROC_, in any case, is the SDK's own spawn protocol and is
	// refused with ReservedEnv before any fork.
	Env []string

	// User is the name or numeric uid to setuid to; empty leaves the parent's.
	// The child takes the user's primary gid unless Group names one. A numeric
	// uid with no passwd entry has no primary gid, so it needs an explicit
	// Group: alone it fails with UnknownUser, never falling back to gid 0.
	User string
	// Group is the name or numeric gid to setgid to. Empty leaves the user's
	// primary gid when User is set, and the parent's when it is not.
	Group string
	// Groups are supplementary group names or gids (systemd SupplementaryGroups=).
	Groups []string

	// Setpgid places the child in its own process group, enabling group-kill of
	// the whole tree (systemd KillMode=control-group without cgroups).
	Setpgid bool
	// Setsid starts the child in a new session detached from the controlling tty.
	Setsid bool
	// DieWithParent, when non-zero, is the signal the child receives when the
	// supervisor dies — a SIGKILL or a crash included, where no Stop runs. Linux and
	// FreeBSD set the kernel's parent-death signal (PR_SET_PDEATHSIG, procctl
	// PROC_PDEATHSIG_CTL), which fires when the THREAD that forked the child exits:
	// the Go runtime keeps its threads, but one whose goroutine ends while locked to
	// it exits with it. Windows places the child in a Job Object with
	// KILL_ON_JOB_CLOSE whose handle stays open for the supervisor's life, so any
	// non-zero signal means termination there. Elsewhere a non-zero value surfaces
	// UnsupportedPlatform before anything is spawned. The zero Signal requests
	// nothing.
	DieWithParent Signal

	// Umask, when non-nil, is the file-creation mask applied to the child
	// (systemd UMask=).
	Umask *int
	// Nice, when non-nil, is the scheduling-priority adjustment applied to the
	// child (systemd Nice=). On Windows it selects the child's priority class at
	// creation: -10 and below HIGH, below 0 ABOVE_NORMAL, 0 NORMAL, 1 to 10
	// BELOW_NORMAL, above 10 IDLE.
	Nice *int
	// OOMScoreAdj, when non-nil, is the /proc/<pid>/oom_score_adj value applied
	// to the child (systemd OOMScoreAdjust=).
	OOMScoreAdj *int
	// Scheduling, when non-nil, is the CPU scheduling policy and the I/O priority
	// applied to the child (systemd CPUSchedulingPolicy=, CPUSchedulingPriority=,
	// IOSchedulingClass=, IOSchedulingPriority=), on the live pid beside Nice. Linux
	// applies both (sched_setscheduler, ioprio_set); FreeBSD applies SchedFIFO,
	// SchedRR and SchedIdle through rtprio(2); every other platform refuses a Policy
	// other than SchedOther or an IOClass other than IOClassNone with
	// UnsupportedPlatform before spawning. An undefined policy or class, or an I/O
	// level past 7, is InvalidSpec; a refusal by the kernel (a realtime policy
	// without CAP_SYS_NICE) is RlimitFailed and the child is torn down.
	Scheduling *SchedulingValue
	// Rlimits are per-resource soft/hard limits applied post-fork, pre-exec
	// (systemd Limit*=); resources unsupported on the platform surface a typed
	// error rather than silently no-op. Without a ResourceNoFile entry the child
	// does not inherit the supervisor's open-file soft limit: the Go runtime
	// raises its own to the hard limit at start and puts the original back in
	// every child it forks, until rlimit.KeepRaisedNoFile is called.
	Rlimits map[Resource]LimitValue

	// CgroupPath, when non-empty, is an already-created cgroup v2 directory the
	// child is placed into BEFORE exec, so controller limits (MemoryMax/TasksMax/
	// …) apply from its first instruction — closing the unconfined window that a
	// post-spawn Group.Add(pid) leaves open (systemd places the unit's payload in
	// its cgroup the same way). On Linux 5.7 and later the child is cloned straight
	// into the group (clone3 with CLONE_INTO_CGROUP); on an older kernel, or one
	// whose filter refuses clone3, the pre-exec trampoline writes its pid to
	// <CgroupPath>/cgroup.procs. Linux-only: a non-empty
	// value on any other GOOS surfaces UnsupportedPlatform; a path that is missing,
	// not a cgroup v2 directory, or not delegated surfaces a typed cgroup error
	// rather than spawning unconfined.
	CgroupPath string

	// Stdio selects how the child's standard streams are wired: StdioInherit
	// (default — share the parent's), StdioNull (discard to the null device),
	// StdioCapture (use the Stdout/Stderr/Stdin members below), or StdioPTY (one
	// pseudo-terminal for all three, sized by PTY). systemd
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
	// PTY, when Stdio is StdioPTY, is the initial window size of the child's
	// pseudo-terminal; nil leaves the size a new terminal has (zero by zero). It is
	// ignored in every other mode.
	PTY *PTYValue
	// DrainGrace bounds how long Wait waits for the StdioCapture copiers once the
	// exit status is in hand. A process the child left behind that inherited its
	// stdout or stderr — a daemon, `sleep 60 &` — keeps the pipe open after the
	// child exits; when the grace closes, Wait closes the parent's pipe ends and
	// returns the real ExitValue with StdioDrainTimeout. The zero value is the
	// bounded default of one second (ADR 0031); a negative value waits for every
	// byte however long the pipes stay open.
	DrainGrace time.Duration

	// ExtraFiles are additional open files inherited by the child, in order,
	// starting at file descriptor 3 (after stdin/stdout/stderr). This is the
	// mechanism behind socket activation (sd_listen_fds): an activator binds
	// listening sockets and passes them here, then sets LISTEN_FDS/LISTEN_PID in
	// Env so the child finds them at fd 3..3+len-1. nil means no extra fds. They
	// are the only descriptors past stdio the child gets, unless FDs says otherwise.
	ExtraFiles []*os.File
	// PIDEnv names variables the child is given set to its own PID, in decimal:
	// the spawn sets them after the fork, before the program runs — what
	// LISTEN_PID and WATCHDOG_PID need, since no parent knows a child's PID
	// before forking it. A name here replaces the same name in Env. Setting one
	// routes the spawn through the trampoline, as Rlimits and Umask do; Windows
	// refuses it with UnsupportedPlatform. A name that is empty or holds '=',
	// ',' or NUL is InvalidSpec, one beginning __KITSUNIUM_SDK_PROC_ ReservedEnv.
	PIDEnv []string
	// FDs says which of the supervisor's descriptors the child inherits besides its
	// stdio and ExtraFiles. FDsSealed — the zero value — gives it none: Start first
	// marks every descriptor of the supervisor from 3 up close-on-exec, one
	// close_range(CLOSE_RANGE_CLOEXEC) on Linux 5.11 and later and on FreeBSD,
	// otherwise a walk of /proc/self/fd (Linux, illumos, Solaris) or /dev/fd (darwin
	// and the BSDs). The mark is process-wide and closes nothing: the supervisor keeps
	// using its descriptors. FDsInherited lets the child inherit every descriptor that
	// lacks close-on-exec, as fork/exec does by default — after a sealed spawn, only
	// those opened without close-on-exec since. Windows passes a child only the
	// handles it is given, so the field changes nothing there.
	FDs FDPolicy
	// Sandbox, when non-nil, is the isolation the child is spawned under:
	// namespaces, mounts, a new root, capabilities, no_new_privs, Landlock and
	// seccomp, applied by the spawn's second stage before the program runs, the
	// seccomp filter last. The identity (User, Group, Groups) is then taken by
	// that stage too, after the capability bounding set is cut, and FDs must be
	// FDsSealed. Linux on amd64 and arm64 applies it; any other platform refuses
	// it with UnsupportedPlatform before anything is spawned, a kernel lacking a
	// part of it with SandboxUnavailable, and a step that fails in the child with
	// SandboxFailed — the program never runs unconfined.
	Sandbox *SandboxValue
}

// FDPolicy says which of the supervisor's descriptors a child inherits besides
// its stdio and ExtraFiles (Spec.FDs). The zero value FDsSealed gives it none.
type FDPolicy uint8

const (
	// FDsSealed gives the child no descriptor of the supervisor's past its stdio and ExtraFiles — the zero value.
	FDsSealed FDPolicy = 0
	// FDsInherited lets the child inherit every descriptor of the supervisor that lacks close-on-exec.
	FDsInherited FDPolicy = 1
)

// PTYValue is a pseudo-terminal's window size in character cells, as TIOCSWINSZ
// sets it and TIOCGWINSZ reads it.
type PTYValue struct {
	// Rows is the number of character rows.
	Rows uint16
	// Cols is the number of character columns.
	Cols uint16
}
