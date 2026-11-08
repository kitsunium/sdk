//go:build unix

package exec

import (
	"context"
	"os"
	"strconv"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/childwait"
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
	spec, err = prepareSpec(spec)
	//: a spec that cannot be honoured here never reaches a fork.
	if err != nil {
		//: the typed refusal verbatim.
		return nil, err
	}
	sio, ioErr := buildStdio(spec)
	//: a stdio fd-setup failure (pipe/null/pty) aborts before credential work.
	if ioErr != nil {
		//: propagate the typed SPAWN_FAILED / PTY_FAILED from the stdio setup verbatim.
		return nil, ioErr
	}
	started, claim, sErr := spawn(spec, sio)
	//: a resolve/attr/fork-exec failure already released the stdio fds.
	if sErr != nil {
		//: propagate the typed RLIMIT_FAILED / UNKNOWN_USER / SPAWN_FAILED verbatim.
		return nil, sErr
	}
	//: the post-start attributes, then the copiers.
	return configure(spec, started, claim, sio)
}

// prepareSpec validates spec, resolves a bare Path in the child's PATH, and
// refuses every field this platform cannot honour — before any OS work, so a
// request is refused, never dropped.
func prepareSpec(spec coreproc.Spec) (coreproc.Spec, error) {
	//: reject a malformed spec (empty Path) before resolving anything.
	if vErr := validateSpec(spec); vErr != nil {
		//: propagate the typed INVALID_SPEC verbatim.
		return spec, vErr
	}
	//: a sandbox this platform or kernel cannot apply never reaches a fork —
	//: nor a PATH search, which a Path inside a new root must not have.
	if sErr := checkSandbox(spec); sErr != nil {
		//: UNSUPPORTED_PLATFORM / SANDBOX_UNAVAILABLE / INVALID_SPEC.
		return spec, sErr
	}
	//: a bare name is searched in the child's PATH, before either spawn path
	//: — fork/exec and the trampoline both need a file, not a name.
	resolved, rErr := resolveSpec(spec)
	//: not found, or found only through a relative PATH entry.
	if rErr != nil {
		//: SPAWN_FAILED carrying os/exec's ErrNotFound or ErrDot.
		return spec, rErr
	}
	//: the field-by-field refusals.
	return resolved, checkSpawnable(resolved)
}

// checkSpawnable runs, in order, the refusals of the fields a platform may not
// honour: an unmapped rlimit or umask, a parent-death signal, a scheduling
// class, a cgroup placement (missing, not a cgroup, off Linux), a
// pseudo-terminal and a PIDEnv name. The first refusal is returned typed.
func checkSpawnable(spec coreproc.Spec) error {
	checks := [...]func(coreproc.Spec) error{
		checkLimits,
		func(s coreproc.Spec) error { return checkDieWithParent(s.DieWithParent) },
		func(s coreproc.Spec) error { return checkScheduling(s.Scheduling) },
		func(s coreproc.Spec) error { return validateCgroupPath(s.CgroupPath) },
		checkPTY,
		func(s coreproc.Spec) error { return checkPIDEnv(s.PIDEnv) },
	}
	//: the first refusal wins; the rest would refuse the same spawn.
	for _, check := range checks {
		//: a field this platform cannot honour.
		if err := check(spec); err != nil {
			//: UNKNOWN_RESOURCE / INVALID_SPEC / UNSUPPORTED_PLATFORM / CGROUP_UNAVAILABLE.
			return err
		}
	}
	//: every requested field is honourable here.
	return nil
}

// configure turns a forked child into the handle Start returns: the
// post-start attributes on the live pid, then the capture copiers. A refused
// attribute tears the child down.
func configure(spec coreproc.Spec, started *os.Process, claim *childwait.Claim, sio *stdioState) (coreproc.Process, error) {
	//: a terminal's child leads a new session, hence its own process group.
	live := newHandle(started, claim, spec.Setpgid || spec.Stdio == coreproc.StdioPTY, sio)
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
// attribute failure leaves no leaked process or zombie behind. The child has
// just been started and may still be inside its execve, where NetBSD up to
// 10.1 discards a signal (PR kern/58091): the SIGKILL is resent until the
// reap (killUntilReaped), so the teardown cannot wait for ever on a signal
// the kernel dropped.
func teardown(live *handle) {
	//: SIGKILL the whole group — the child may already have forked — until the
	//: leader is reaped. Nothing cancels a teardown: the resend bounds it by
	//: the child's death. A delivery fault is cleanup detail, not the caller's
	//: error, which is the attribute failure being reported.
	swallowErr(live.killUntilReaped(context.Background()))
}

// spawn resolves the spawn target (direct or via the limits trampoline),
// assembles the os.ProcAttr, and forks/execs. On any failure it releases the
// stdio fds (so the aborted spawn leaks nothing) and returns the typed error; on
// success it returns the live OS process, still awaiting post-start attributes,
// and the claim on its exit status (ADR 0093).
func spawn(spec coreproc.Spec, sio *stdioState) (started *os.Process, claim *childwait.Claim, err error) {
	path, argv, envAddon, rErr := resolveSpawn(spec)
	//: a trampoline-setup failure (no self-path for the limits) aborts the spawn.
	if rErr != nil {
		//: release the stdio fds so the aborted spawn leaks nothing.
		sio.closeAll()
		//: propagate the typed RLIMIT_FAILED from the trampoline resolution.
		return nil, nil, rErr
	}
	files, handshakePipe, hErr := spawnFiles(spec, sio)
	//: a handshake-pipe setup failure aborts before fork/exec.
	if hErr != nil {
		//: release the stdio fds so the aborted spawn leaks nothing.
		sio.closeAll()
		//: propagate the typed RLIMIT_FAILED wrapping the pipe cause.
		return nil, nil, hErr
	}
	//: assemble stdio + ExtraFiles + handshake (last) and the handshake-fd env so
	//: ExtraFiles keep fd 3.. and the trampoline still reports through the pipe.
	childFiles, hsAddon := childFileTable(files, spec.ExtraFiles, handshakePipe)
	attr, aErr := buildProcAttr(spec, childFiles, append(envAddon, hsAddon...))
	//: a credential-resolution failure aborts before fork/exec.
	if aErr != nil {
		//: release the handshake pipe and stdio fds so nothing leaks.
		closeHandshake(handshakePipe)
		sio.closeAll()
		//: propagate the typed UNKNOWN_USER / UNKNOWN_GROUP verbatim.
		return nil, nil, aErr
	}
	payload, pErr := attachSandbox(spec, attr)
	//: the sandbox's payload could not be sealed.
	if pErr != nil {
		//: release the handshake pipe and stdio fds so nothing leaks.
		closeHandshake(handshakePipe)
		sio.closeAll()
		//: SANDBOX_UNAVAILABLE naming memfd_create.
		return nil, nil, pErr
	}
	//: seal, fork, and read the outcome.
	return forkPrepared(spec, sio, handshakePipe, launch{path: path, argv: argv, attr: attr, payload: payload})
}

// launch is what a prepared spawn hands os.StartProcess, and the sandbox
// payload the fork hands the child, released once the fork is done.
type launch struct {
	path    string
	argv    []string
	attr    *os.ProcAttr
	payload *os.File
}

// release closes the parent's copy of the sandbox payload, if any: the child
// holds its own from the fork on.
func (l launch) release() {
	//: no sandbox, no payload.
	if l.payload != nil {
		swallowErr(l.payload.Close())
	}
}

// forkPrepared seals the supervisor's descriptor table, attaches the cgroup
// the clone places the child in, forks, and reads the outcome: a failure
// releases what the spawn holds (a clone into a filtered clone3 is retried
// through the trampoline), a trampoline is awaited until it execs the target.
func forkPrepared(spec coreproc.Spec, sio *stdioState, handshakePipe *handshake, l launch) (*os.Process, *childwait.Claim, error) {
	//: the child's table is its stdio and ExtraFiles: every other descriptor of
	//: the supervisor is marked close-on-exec first, unless Spec.FDs inherits.
	if sealErr := sealInherited(spec); sealErr != nil {
		//: release the handshake pipe and stdio fds before surfacing the failure.
		l.release()
		closeHandshake(handshakePipe)
		sio.closeAll()
		//: the typed SPAWN_FAILED naming the descriptor walk or range.
		return nil, nil, sealErr
	}
	//: a cgroup placement the clone makes: the group's directory for the fork.
	release, gErr := attachCgroupFD(spec, l.attr.Sys)
	//: the group's directory vanished since it was validated.
	if gErr != nil {
		//: release the handshake pipe and stdio fds before surfacing the failure.
		l.release()
		closeHandshake(handshakePipe)
		sio.closeAll()
		//: the typed CGROUP_UNAVAILABLE.
		return nil, nil, gErr
	}
	cloned := cgroupByClone(spec.CgroupPath)
	proc, claim, sErr := forkClaimed(l.path, l.argv, l.attr)
	//: the fork holds its own reference to the group's directory and payload.
	release()
	l.release()
	//: a fork/exec failure is typed, or retried through the trampoline.
	if sErr != nil {
		//: release the handshake pipe before surfacing the failure.
		closeHandshake(handshakePipe)
		//: SPAWN_FAILED, CGROUP_WRITE_FAILED, or the trampoline's retry.
		return forkFailed(spec, sio, cloned, sErr)
	}
	//: a trampolined spawn that failed before exec leaves nothing to hand back.
	if awaitErr := awaitTrampoline(handshakePipe, proc, claim, sio); awaitErr != nil {
		//: surface the trampoline's typed RLIMIT_FAILED / SPAWN_FAILED.
		return nil, nil, awaitErr
	}
	//: the forked, live OS process, still awaiting post-start attributes.
	return proc, claim, nil
}

// forkFailed reads a fork/exec failure: a clone into a group that clone3's
// filter refused is spawned again through the trampoline (the stdio is still
// intact); any other failure releases the stdio and is typed —
// CGROUP_WRITE_FAILED for a group that refused the member, SPAWN_FAILED else.
func forkFailed(spec coreproc.Spec, sio *stdioState, cloned bool, cause error) (*os.Process, *childwait.Claim, error) {
	//: a plain fork/exec failure.
	if !cloned {
		sio.closeAll()
		//: wrap the StartProcess cause under the central SPAWN_FAILED fields.
		return nil, nil, wrapSpawn(cause, errs.String("path", spec.Path))
	}
	retry, cErr := cloneSpawnError(cause, spec.CgroupPath, spec.Path, &cloneIntoCgroupRefused)
	//: clone3 is filtered: the trampoline places the child instead.
	if retry {
		//: the retry builds the trampoline's files.
		return spawn(spec, sio)
	}
	sio.closeAll()
	//: CGROUP_WRITE_FAILED or SPAWN_FAILED.
	return nil, nil, cErr
}

// awaitTrampoline blocks a trampolined spawn on its handshake until the child
// execs the target (EOF, nil) or reports a pre-exec failure, which it returns
// as the typed RLIMIT_FAILED / SPAWN_FAILED after cleaning up: the exited
// trampoline child is reaped — through the claim, since a running reaper may
// already have collected it — and the stdio fds are released, so the aborted
// spawn leaks nothing. A spawn with no trampoline has no handshake: nil.
func awaitTrampoline(hs *handshake, proc releaser, claim *childwait.Claim, sio *stdioState) error {
	//: an untrampolined spawn exec'd the target directly.
	if hs == nil {
		//: nothing to wait for.
		return nil
	}
	awaitErr := hs.await()
	//: EOF: the child exec'd the target.
	if awaitErr == nil {
		//: the spawn stands.
		return nil
	}
	//: reap the exited trampoline child so it leaves no zombie.
	_, swept, wErr := collectExit(proc, claim)
	swallowErr(wErr)
	//: a swept status left proc unwaited; no handle exists yet to race it.
	if swept {
		swallowErr(proc.Release())
	}
	//: release the stdio fds so the aborted spawn leaks nothing.
	sio.closeAll()
	//: the trampoline's typed failure.
	return awaitErr
}

// forkClaimed forks/execs through the child ledger, so the child's exit status
// is claimed before a running reaper can collect it as an orphan's: the child
// may exit — and the reaper sweep — before os.StartProcess has even returned.
func forkClaimed(path string, argv []string, attr *os.ProcAttr) (started *os.Process, claim *childwait.Claim, err error) {
	//: the ledger runs the fork under its spawn gate and claims the pid.
	return childwait.Spawn(func() (*os.Process, error) {
		//: the one fork/exec of the Unix spawn.
		return os.StartProcess(path, argv, attr)
	})
}

// spawnFiles returns the stdio file table and, for a trampolined spawn, the
// handshake pipe. It deliberately does NOT place the pipe in the table: spawn
// appends it AFTER any Spec.ExtraFiles so the extras keep their documented fd 3..
// positions (socket activation), with the handshake landing at fd 3+len(extras).
func spawnFiles(spec coreproc.Spec, sio *stdioState) (files []*os.File, hs *handshake, err error) {
	//: a direct spawn carries only the three wired std streams.
	if !trampolined(spec) {
		//: no trampoline, so no handshake pipe is needed.
		return sio.files[:], nil, nil
	}
	pipe, hErr := newHandshake()
	//: a pipe-setup failure means the trampoline outcome cannot be observed.
	if hErr != nil {
		//: wrap the os.Pipe cause as the central RLIMIT_FAILED sentinel.
		return nil, nil, wrapRlimit(hErr, errs.String("path", spec.Path))
	}
	//: hand back stdio + the pipe handle; spawn positions the pipe after ExtraFiles.
	return sio.files[:], pipe, nil
}

// childFileTable assembles the child's full fd table — stdio (0-2), then
// Spec.ExtraFiles at fd 3.. (socket activation), then the handshake pipe LAST so
// the extras keep their contracted positions. When a handshake is present it
// also returns the env entry carrying the pipe's descriptor (3+len(ExtraFiles))
// so the trampoline reports through the right fd; the slice has no handshake and
// the addon is empty for a direct spawn.
func childFileTable(stdio, extra []*os.File, hs *handshake) (files []*os.File, hsAddon []string) {
	//: extras inherit at fd 3+ exactly as os/exec.Cmd.ExtraFiles documents.
	files = appendExtraFiles(stdio, extra)
	//: a direct spawn has no handshake pipe and needs no fd addon.
	if hs == nil {
		//: stdio+extras only; no sentinel fd entry.
		return files, nil
	}
	//: the pipe goes last; its fd is the current length (3+len(ExtraFiles)).
	hsAddon = []string{trampolineHsFdEnv + "=" + strconv.Itoa(len(files))}
	//: append the write end after the extras so they keep fd 3...
	files = append(files, hs.childFile())
	//: the full table plus the fd addon for the trampoline.
	return files, hsAddon
}

// resolveSpawn returns the (path, argv, envAddon) for os.StartProcess. A spec
// with no pre-exec limits spawns the target verbatim with no env addon; a spec
// setting an Rlimit or a Umask spawns the re-exec trampoline (this binary)
// carrying the encoded limits, which applies them then execs the real target.
func resolveSpawn(spec coreproc.Spec) (path string, argv, envAddon []string, err error) {
	//: no rlimit/umask requested — spawn the target directly, no trampoline.
	if !trampolined(spec) {
		//: direct spawn: target path, its argv, and no extra environment.
		return spec.Path, buildArgv(spec), nil, nil
	}
	self, tArgv, addon, tErr := trampolineSpawn(spec)
	//: a missing self-path means the limits cannot be honoured pre-exec.
	if tErr != nil {
		//: wrap the os.Executable failure as the central RlimitFailed sentinel.
		return "", nil, nil, wrapRlimit(tErr, errs.String("path", spec.Path))
	}
	//: trampoline spawn: this binary, argv [self, target, argv...], + sentinel env.
	return self, tArgv, addon, nil
}

// buildProcAttr assembles the os.ProcAttr for the spawn: the working directory,
// the explicit environment (empty, never inherited, when Spec.Env is nil) plus
// any trampoline sentinel entry from envAddon, the inherited std streams, and the
// SysProcAttr carrying credentials and the group/session topology.
func buildProcAttr(spec coreproc.Spec, files []*os.File, envAddon []string) (attr *os.ProcAttr, err error) {
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
	//: the parent-death signal, where the kernel has one (checked before).
	setDieWithParent(sysAttr, spec.DieWithParent)
	//: a terminal's child leads a new session with the slave (its fd 0) as its
	//: controlling terminal; setpgid would fail for a session leader.
	if spec.Stdio == coreproc.StdioPTY {
		sysAttr.Setsid, sysAttr.Setctty, sysAttr.Ctty, sysAttr.Setpgid = true, true, fdStdin, false
	}

	//: os.StartProcess inherits the parent environment ONLY when Env is nil, so the
	//: child's environment is always built on a fresh non-nil slice — even an empty
	//: one — to honour the port's "explicit, never inherited" guarantee (a nil here
	//: would leak the supervisor's environment, secrets included). Building anew
	//: also keeps the trampoline's sentinel entry from mutating the caller's
	//: immutable Spec.Env backing array. Each variable once, its last value: a
	//: child's getenv reads the first entry of a name.
	env := dedupEnv(spec.Env)
	//: then the trampoline sentinel entry (if any); it is stripped before execve.
	env = append(env, envAddon...)

	//: files is the already-assembled child table (stdio + ExtraFiles + any
	//: handshake), built by childFileTable; the attr is ready to spawn.
	return &os.ProcAttr{Dir: spec.Dir, Env: env, Files: files, Sys: sysAttr}, nil
}

// appendExtraFiles returns std (the three std fds) followed by the caller's
// extra inherited files at fd 3+, without mutating either input slice.
func appendExtraFiles(std, extra []*os.File) []*os.File {
	//: no extras means the child inherits only its three standard streams.
	if len(extra) == 0 {
		//: return the std slice unchanged.
		return std
	}
	//: a fresh slice fd 0-2 = std, fd 3+ = extra; neither input is aliased.
	out := make([]*os.File, 0, len(std)+len(extra))
	out = append(out, std...)
	out = append(out, extra...)
	//: the child now inherits std streams plus the activator's sockets.
	return out
}

// buildArgv returns the argv for the spawn: the caller-supplied Args verbatim,
// or [coreproc.Spec.Path] when Args is empty (the port's argv[0] default).
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
// set on a live pid (Nice, OOMScoreAdj, Scheduling). The first refusal returns its typed
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
	//: the scheduling policy and I/O priority last, beside Nice.
	return applyScheduling(pid, spec.Scheduling)
}
