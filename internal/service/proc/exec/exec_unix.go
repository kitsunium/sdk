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

	sio, ioErr := buildStdio(spec)
	//: a stdio fd-setup failure (pipe/null) aborts before credential work.
	if ioErr != nil {
		//: propagate the typed SPAWN_FAILED from the stdio setup verbatim.
		return nil, ioErr
	}

	attr, aErr := buildProcAttr(spec, sio.files[:])
	//: a credential-resolution failure aborts before fork/exec.
	if aErr != nil {
		//: release the stdio fds so the aborted spawn leaks nothing.
		sio.closeAll()
		//: propagate the typed UNKNOWN_USER / UNKNOWN_GROUP verbatim.
		return nil, aErr
	}

	started, sErr := os.StartProcess(spec.Path, buildArgv(spec), attr)
	//: a fork/exec failure is a typed SPAWN_FAILED wrapping the OS cause.
	if sErr != nil {
		//: release the stdio fds before surfacing the spawn failure.
		sio.closeAll()
		//: wrap the StartProcess cause under the central SPAWN_FAILED fields.
		return nil, wrapSpawn(sErr, errs.String("path", spec.Path))
	}

	live := newHandle(started, spec.Setpgid, sio)
	//: best-effort scheduling attributes run post-start on the live pid. They run
	//: BEFORE the capture copiers launch, so a teardown here never blocks on a
	//: caller's sink: with no copiers started, Wait's copier-join is a no-op.
	if pErr := applyPostStart(live.pid, spec); pErr != nil {
		//: kill + reap the child; no copiers are running, so this cannot hang.
		teardown(live)
		//: release the (still-unstarted) stdio fds so nothing leaks.
		sio.closeAll()
		//: propagate the typed RLIMIT_FAILED from the attribute application.
		return nil, pErr
	}
	//: the child is fully configured — close the parent's child-side ends and
	//: launch the capture copiers (joined by Wait for 100% delivery).
	sio.afterStart()
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
func buildProcAttr(spec coreproc.Spec, files []*os.File) (attr *os.ProcAttr, err error) {
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

	//: os.StartProcess inherits the parent environment ONLY when Env is nil. A nil
	//: Spec.Env must therefore become a non-nil zero-length slice to honour the
	//: port's "explicit, never inherited" guarantee — a genuinely nil slice here
	//: would leak the supervisor's environment (secrets included) into the child,
	//: so make() is deliberate over the lint-preferred nil.
	env := spec.Env
	//: map a nil Env to a non-nil empty slice (see the security note above).
	if env == nil {
		//: force an empty-but-non-nil environment so nothing is inherited.
		env = make([]string, 0)
	}

	//: the child's std streams were wired by buildStdio per Spec.Stdio (inherit,
	//: null, or capture); the assembled attr is ready for os.StartProcess.
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
