//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/process .

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
// # Environment
//
// A nil Spec.Env yields an empty environment — the child never silently inherits
// the supervisor's, so a spawned service starts from a known state. Pass an
// explicit slice (including os.Environ()) to inherit deliberately.
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
// On non-Unix platforms Start returns UnsupportedPlatform; the package compiles
// everywhere but only supervises on Unix.
package process

import (
	"context"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// Spec is the immutable description of a process to spawn — executable,
// environment, credentials, isolation topology, and scheduling attributes. It is
// an alias of the core port type.
type Spec = coreproc.Spec

// Process is the handle to a spawned process: PID, Wait, Signal, SignalGroup,
// and a group-aware Stop. It is an alias of the core port interface.
type Process = coreproc.Process

// ExitResult is the outcome of a finished process — exit code, terminating
// signal, and resource usage. It is an alias of the core port's ExitValue.
type ExitResult = coreproc.ExitValue

// Signal is a typed, platform-portable OS signal. It is an alias of the core
// port type, so process.SIGTERM and a signal parsed elsewhere compare equal.
type Signal = coreproc.Signal

// Resource identifies a per-process resource governed by setrlimit(2). It is an
// alias of the core port type.
type Resource = coreproc.Resource

// Limit is a soft/hard resource-limit pair for setrlimit(2). It is an alias of
// the core port's LimitValue.
type Limit = coreproc.LimitValue

// Start spawns the process described by spec and returns a live Process handle.
// It delegates to internal/service/proc/exec; ctx is honoured up to the
// fork/exec boundary. On non-Unix platforms it returns UnsupportedPlatform.
func Start(ctx context.Context, spec Spec) (proc Process, err error) {
	//: the facade adds no behaviour — delegate straight to the service spawn.
	return svcexec.Start(ctx, spec)
}

// MustStart is like [Start] but panics with the typed error when the spawn fails
// — UnsupportedPlatform off Unix/Windows, InvalidSpec, RlimitFailed, … It is the
// idiomatic Go MustX opt-in (like [regexp.MustCompile]) for a consumer that
// chooses crash-on-failure at its own startup; the SDK itself never panics, and
// [Start] is the non-panicking form for normal use. The panic value is the typed
// error, so a top-level recover() can classify it via errs.CodeOf / HasCode.
func MustStart(ctx context.Context, spec Spec) Process {
	p, err := Start(ctx, spec)
	//: a failed spawn is the consumer's chosen crash point.
	if err != nil {
		//: panic with the typed error value, never a bare string.
		panic(err)
	}
	//: the live process handle on success.
	return p
}
