//go:build unix

// Package exec — the Unix spawn: validates the Spec, resolves credentials,
// builds the SysProcAttr (Setpgid/Setsid/Credential), forks/execs via
// os.StartProcess, then applies best-effort scheduling attributes. Returns a
// *Handle satisfying the coreproc.Process port.
package exec

import (
	"context"
	"os"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Start spawns the process described by spec and returns a live coreproc.Process
// handle. ctx is honoured up to the fork/exec boundary: a ctx already cancelled
// is reported before any OS work. The child runs with an explicit (possibly
// empty) environment, the requested credentials, and — when Setpgid is set — its
// own process group so the whole tree can be group-killed.
func Start(ctx context.Context, spec coreproc.Spec) (proc coreproc.Process, err error) {
	//: a cancelled context short-circuits before touching the OS.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: surface the cancellation as the spawn error verbatim.
		return nil, ctxErr
	}
	//: reject a malformed spec (empty Path) before resolving anything.
	if vErr := validateSpec(spec); vErr != nil {
		//: propagate the typed INVALID_SPEC verbatim.
		return nil, vErr
	}
	//: reject unhonourable umask/rlimit fields up front rather than dropping them.
	if lErr := checkLimits(spec); lErr != nil {
		//: propagate the typed UNKNOWN_RESOURCE / RLIMIT_FAILED verbatim.
		return nil, lErr
	}

	attr, aErr := buildProcAttr(spec)
	//: a credential-resolution failure aborts before fork/exec.
	if aErr != nil {
		//: propagate the typed UNKNOWN_USER / UNKNOWN_GROUP verbatim.
		return nil, aErr
	}

	started, sErr := os.StartProcess(spec.Path, buildArgv(spec), attr)
	//: a fork/exec failure is a typed SPAWN_FAILED wrapping the OS cause.
	if sErr != nil {
		//: wrap the StartProcess cause under the central SPAWN_FAILED fields.
		return nil, wrapSpawn(sErr, errs.String("path", spec.Path))
	}

	live := newHandle(started)
	//: best-effort scheduling attributes run post-start on the live pid.
	if pErr := applyPostStart(live.pid, spec); pErr != nil {
		//: a refused attribute tears the child down so nothing half-configured leaks.
		teardown(live)
		//: propagate the typed RLIMIT_FAILED from the attribute application.
		return nil, pErr
	}
	//: a fully-configured, running process handle.
	return live, nil
}

// teardown kills and reaps a partially-configured child so a post-start
// attribute failure leaves no leaked process or zombie behind.
func teardown(live *handle) {
	//: SIGKILL the whole group — the child may already have forked.
	if err := live.SignalGroup(coreproc.Signal(syscall.SIGKILL)); err != nil {
		//: a group already gone needs no reap; nothing more to do.
		if processGone(err) {
			//: the child exited before we could kill it — already clean.
			return
		}
	}
	//: drive the reap so the killed child does not linger as a zombie.
	if _, err := live.Wait(); err != nil {
		//: the reap fault is best-effort cleanup detail, not the caller's error.
		return
	}
}

// buildProcAttr assembles the os.ProcAttr for the spawn: the working directory,
// the explicit environment (empty, never inherited, when Spec.Env is nil), the
// inherited std streams, and the SysProcAttr carrying credentials and the
// group/session topology.
func buildProcAttr(spec coreproc.Spec) (attr *os.ProcAttr, err error) {
	cred, cErr := resolveCredential(spec)
	//: a credential-resolution failure aborts attr assembly.
	if cErr != nil {
		//: propagate the typed identity error verbatim.
		return nil, cErr
	}

	sysAttr := &syscall.SysProcAttr{
		Setpgid:    spec.Setpgid,
		Setsid:     spec.Setsid,
		Credential: cred,
	}

	//: nil Env yields an empty environment (the port's known-state guarantee),
	//: never the supervisor's; a non-nil slice is used verbatim. A nil slice and
	//: an explicit empty slice both spawn with no environment, which is the intent.
	var env []string
	//: a non-nil Env is honoured exactly; nil stays the empty-environment default.
	if spec.Env != nil {
		//: use the caller's explicit environment verbatim.
		env = spec.Env
	}

	//: inherit the supervisor's std streams; redirection is a caller concern.
	files := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	//: the assembled attr is ready for os.StartProcess.
	return &os.ProcAttr{Dir: spec.Dir, Env: env, Files: files, Sys: sysAttr}, nil
}

// buildArgv returns the argv for the spawn: the caller-supplied Args verbatim,
// or [Path] when Args is empty (the port's argv[0] default).
func buildArgv(spec coreproc.Spec) []string {
	//: an empty Args defaults argv to the executable path alone.
	if len(spec.Args) == 0 {
		//: argv[0] defaults to the executable when the caller gives no args.
		return []string{spec.Path}
	}
	//: the caller supplied a full argv (including argv[0]); use it verbatim.
	return spec.Args
}

// applyPostStart applies the best-effort scheduling attributes that can only be
// set on a live pid (Nice, OOMScoreAdj). The first refusal returns its typed
// error so the caller can tear the child down.
func applyPostStart(pid int, spec coreproc.Spec) error {
	//: scheduling priority first — a refusal here aborts before the OOM bias.
	if err := applyNice(pid, spec.Nice); err != nil {
		//: propagate the typed RLIMIT_FAILED from setpriority.
		return err
	}
	//: OOM bias second; a procfs write failure is likewise typed.
	if err := applyOOMScoreAdj(pid, spec.OOMScoreAdj); err != nil {
		//: propagate the typed RLIMIT_FAILED from the procfs write.
		return err
	}
	//: both attributes (or the absence of either) applied cleanly.
	return nil
}
