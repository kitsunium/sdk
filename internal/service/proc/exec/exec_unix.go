//go:build unix

// Package exec — the Unix spawn: validates the Spec, resolves credentials,
// builds the SysProcAttr (Setpgid/Setsid/Credential), forks/execs via
// os.StartProcess, then applies best-effort scheduling attributes. Returns a
// *Handle satisfying the coreproc.Process port.
package exec

import (
	"context"
	"os"
	"strconv"
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
	//: reject a missing / non-cgroup / off-platform CgroupPath before spawning,
	//: so a placement that cannot succeed never starts an unconfined child.
	if cErr := validateCgroupPath(spec.CgroupPath); cErr != nil {
		//: propagate the typed CGROUP_UNAVAILABLE / UNSUPPORTED_PLATFORM verbatim.
		return nil, cErr
	}

	sio, ioErr := buildStdio(spec)
	//: a stdio fd-setup failure (pipe/null) aborts before credential work.
	if ioErr != nil {
		//: propagate the typed SPAWN_FAILED from the stdio setup verbatim.
		return nil, ioErr
	}

	started, sErr := spawn(spec, sio)
	//: a resolve/attr/fork-exec failure already released the stdio fds.
	if sErr != nil {
		//: propagate the typed RLIMIT_FAILED / UNKNOWN_USER / SPAWN_FAILED verbatim.
		return nil, sErr
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

// spawn resolves the spawn target (direct or via the limits trampoline),
// assembles the os.ProcAttr, and forks/execs. On any failure it releases the
// stdio fds (so the aborted spawn leaks nothing) and returns the typed error; on
// success it returns the live OS process, still awaiting post-start attributes.
func spawn(spec coreproc.Spec, sio *stdioState) (started *os.Process, err error) {
	path, argv, envAddon, rErr := resolveSpawn(spec)
	//: a trampoline-setup failure (no self-path for the limits) aborts the spawn.
	if rErr != nil {
		//: release the stdio fds so the aborted spawn leaks nothing.
		sio.closeAll()
		//: propagate the typed RLIMIT_FAILED from the trampoline resolution.
		return nil, rErr
	}

	files, hs, hErr := spawnFiles(spec, sio)
	//: a handshake-pipe setup failure aborts before fork/exec.
	if hErr != nil {
		//: release the stdio fds so the aborted spawn leaks nothing.
		sio.closeAll()
		//: propagate the typed RLIMIT_FAILED wrapping the pipe cause.
		return nil, hErr
	}

	//: assemble stdio + ExtraFiles + handshake (last) and the handshake-fd env so
	//: ExtraFiles keep fd 3.. and the trampoline still reports through the pipe.
	childFiles, hsAddon := childFileTable(files, spec.ExtraFiles, hs)
	attr, aErr := buildProcAttr(spec, childFiles, append(envAddon, hsAddon...))
	//: a credential-resolution failure aborts before fork/exec.
	if aErr != nil {
		//: release the handshake pipe and stdio fds so nothing leaks.
		closeHandshake(hs)
		sio.closeAll()
		//: propagate the typed UNKNOWN_USER / UNKNOWN_GROUP verbatim.
		return nil, aErr
	}
	proc, sErr := os.StartProcess(path, argv, attr)
	//: a fork/exec failure is a typed SPAWN_FAILED wrapping the OS cause.
	if sErr != nil {
		//: release the handshake pipe and stdio fds before surfacing the failure.
		closeHandshake(hs)
		sio.closeAll()
		//: wrap the StartProcess cause under the central SPAWN_FAILED fields.
		return nil, wrapSpawn(sErr, errs.String("path", spec.Path))
	}
	//: a trampolined spawn blocks on the handshake until the child execs the
	//: target (EOF) or reports a pre-exec failure (typed RLIMIT_FAILED/SPAWN_FAILED).
	if hs != nil {
		//: surface a trampoline pre-exec failure as the typed sentinel.
		if awaitErr := hs.await(); awaitErr != nil {
			//: reap the exited trampoline child so it leaves no zombie.
			_, wErr := proc.Wait()
			swallowErr(wErr)
			//: release the stdio fds so the aborted spawn leaks nothing.
			sio.closeAll()
			//: surface the trampoline's typed RLIMIT_FAILED / SPAWN_FAILED.
			return nil, awaitErr
		}
	}
	//: the forked, live OS process, still awaiting post-start attributes.
	return proc, nil
}

// spawnFiles returns the stdio file table and, for a trampolined spawn, the
// handshake pipe. It deliberately does NOT place the pipe in the table: spawn
// appends it AFTER any Spec.ExtraFiles so the extras keep their documented fd 3..
// positions (socket activation), with the handshake landing at fd 3+len(extras).
func spawnFiles(spec coreproc.Spec, sio *stdioState) (files []*os.File, hs *handshake, err error) {
	//: a direct spawn carries only the three wired std streams.
	if !needsTrampoline(spec) {
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
	if !needsTrampoline(spec) {
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

	//: os.StartProcess inherits the parent environment ONLY when Env is nil, so the
	//: child's environment is always built on a fresh non-nil slice — even an empty
	//: one — to honour the port's "explicit, never inherited" guarantee (a nil here
	//: would leak the supervisor's environment, secrets included). Building anew
	//: also keeps the trampoline's sentinel entry from mutating the caller's
	//: immutable Spec.Env backing array.
	env := make([]string, 0, len(spec.Env)+len(envAddon))
	//: the caller's explicit environment first (a nil Spec.Env appends nothing).
	env = append(env, spec.Env...)
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
