// Package process is the public facade for the SDK's keystone process-spawn
// primitive: it starts a child process under explicit credentials, in its own
// process group and session, and returns a handle that can wait for exit, signal
// the leader or its whole group, and stop the group gracefully with a
// SIGTERM→SIGKILL escalation.
//
// It is a thin facade over internal/service/proc/exec. The public types are
// aliases of the internal/core/proc port, so a value built here is the same type
// the service layer consumes — no conversion, no parallel hierarchy.
//
// # Spec → systemd directive mapping
//
//	Spec.Path / Args / Dir / Env   ExecStart= / WorkingDirectory= / Environment=
//	Spec.User / Group / Groups     User= / Group= / SupplementaryGroups=
//	Spec.Setpgid                   KillMode=control-group (group-killable tree)
//	Spec.Setsid                    a new session, detached from the parent tty
//	Spec.Nice                      Nice=
//	Spec.OOMScoreAdj               OOMScoreAdjust=
//	Spec.Umask                     UMask=        (applied via trampoline)
//	Spec.Rlimits                   Limit*=       (applied via trampoline)
//
// # Environment, and finding the executable
//
// A nil Spec.Env yields an empty environment — the child never silently inherits
// the supervisor's, so a spawned service starts from a known state. Pass an
// explicit slice (including os.Environ()) to inherit deliberately.
//
// Spec.Path may be a bare name. "go" is searched in the PATH the CHILD will
// see — the PATH entry of Spec.Env whenever it names one, even empty, the
// parent's only when it names none —
// with os/exec's rules: the first executable in PATH order wins, and a match
// found only through a relative entry ("." or an empty one) is refused with
// exec.ErrDot, which the returned SpawnFailed wraps (errors.Is answers). A path
// with a separator is taken as written.
//
// # Standard streams
//
// Spec.Stdio selects how the child's stdin/stdout/stderr are wired:
// StdioInherit (the default) shares the parent's streams; StdioNull discards the
// child's output and gives it an immediate-EOF stdin; StdioCapture connects
// Spec.Stdout and Spec.Stderr (any io.Writer) and Spec.Stdin (any io.Reader),
// with a nil stream falling back to the null device for that one stream. In
// capture mode every byte the child writes reaches the writers before Wait
// returns, and the copier goroutines terminate at EOF (no leak). It composes
// with Setpgid/Setsid and credential drop. Stdin should be EOF-terminating.
//
// # Stopping a process group
//
// Stop sends the chosen signal to the entire process group, waits up to grace
// for exit, then escalates to SIGKILL on the group. Because the child leads its
// own group (Spec.Setpgid), forked grandchildren are reached too — no survivor
// is left behind.
//
// # Example
//
//	spec := process.Spec{
//		Path:    "/bin/sh",
//		Args:    []string{"sh", "-c", "sleep 1000 & wait"},
//		Setpgid: true,
//	}
//	p, err := process.Start(context.Background(), spec)
//	if err != nil {
//		// err carries a central proc sentinel code (errs.HasCode).
//		return err
//	}
//	// Graceful stop: SIGTERM to the group, escalate to SIGKILL after 5s.
//	if err := p.Stop(context.Background(), 5*time.Second, process.SIGTERM); err != nil {
//		return err
//	}
//	exit, err := p.Wait()
//	_ = exit // exit.Code / exit.Signal / exit.UserTime / exit.MaxRSS
//
// # Resource limits and umask
//
// The Go runtime exposes no hook to run setrlimit(2) or umask(2) in the child
// between fork and exec, so Start honours Spec.Rlimits and Spec.Umask via a
// re-exec trampoline: when either is set, Start spawns this binary as a tiny
// self-trampoline that applies the limits in the fresh child, then execs the real
// target — so the target starts already under its limits, with no cgo and no
// dependency. An Rlimit naming a resource with no RLIMIT_* mapping on the platform
// (NPROC, MEMLOCK — absent from stdlib syscall) is still rejected up front with
// UnknownResource; a limit the kernel refuses to set (e.g. raising a hard cap
// unprivileged) fails the spawn rather than running the child unconfined. Nice and
// OOMScoreAdj are applied post-start on the live pid and surface a typed error if
// the host refuses.
//
// # Platforms
//
// Unix gets everything above. Windows spawns too, through CreateProcess: stdio
// wiring, Setpgid (a new console process group), Spec.Rlimits (enforced by a
// Job Object) and the PATH search (with PATHEXT) all work, while the fields
// with no Windows meaning at this layer — User/Group/Groups, Umask, Nice,
// OOMScoreAdj, ExtraFiles, CgroupPath — are refused with UnsupportedPlatform
// rather than dropped. There, SignalGroup reaches the leader only and Stop's
// escalation is TerminateProcess. Every other platform returns
// UnsupportedPlatform from Start; the package compiles everywhere.
//
// # The process itself
//
// Everything above acts on a child. [Self] and [Build] read the process they
// run in, on every platform, and never fail:
//
//	stats := process.Self()
//	stats.Goroutines, stats.HeapBytes, stats.GCPauses.Quantile(0.99), stats.CPUTime
//
//	build, ok := process.Build()
//	sdk, found := build.Module("github.com/kitsunium/sdk")
//	// sdk.Version is a release ("v0.4.6"), or "" with sdk.Revision/sdk.Time
//	// for a pseudo-version, or "" with sdk.Local for a directory.
//
// CPUTime is the kernel's count (getrusage) where the platform has one and the
// Go runtime's estimate elsewhere — refreshed only at a garbage collection;
// Stats.CPUEstimated says which. The
// distributions and every counter are cumulative since the process started —
// subtract two snapshots for a window.
//
// A module's recorded version conflates three things, and [Module] keeps them
// apart: a RELEASE in Version, a COMMIT in Revision and Time (a pseudo-version
// names one, and so does the main module's version-control stamp), and a
// DIRECTORY in Local and Dir (a replace, a workspace module). What is in a
// local directory NOW is a question for the framework's git.Head (ADR 0158).
//
// Package process — the running process itself: what it was built from and
// what it is doing.
//
// Package process — ergonomic re-exports: the handful of signal constants and
// resource sentinels callers need to drive Stop/SignalGroup and read typed
// errors without importing internal/core/proc directly.
//
// Package process — StdioMode re-exports for wiring a child's standard streams.
package process
