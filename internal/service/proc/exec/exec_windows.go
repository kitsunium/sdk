//go:build windows

// Package exec — the Windows spawn primitive. Start turns a Spec into a running,
// supervised process via os.StartProcess (CreateProcess under the hood) and
// returns a coreproc.Process handle. Stdio (inherit/null/capture) reuses the
// portable stdioState; Setpgid maps to a new console process group so a later
// CTRL_BREAK can target the child. The Unix-only Spec fields — rlimits, umask,
// nice/oom, credentials, ExtraFiles, a cgroup path — have no Windows equivalent
// in this layer (resource confinement is the Job Object backend's job, applied at
// the cgroup port), so they are rejected up front with the uniform sentinel
// rather than silently dropped.
package exec

import (
	"context"
	"os"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP (winbase.h): the child leads a
// new console process group, the precondition for a CTRL_BREAK group signal.
const createNewProcessGroup uint32 = 0x00000200

// Start spawns spec as a supervised Windows process and returns its handle. A
// cancelled context, a malformed Spec, or an unsupported Spec field short-circuit
// before any process is created.
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
	//: reject the Unix-only fields with no Windows equivalent before spawning, so
	//: no requested confinement/credential is silently dropped.
	if uErr := checkUnsupportedSpec(spec); uErr != nil {
		//: propagate the uniform UNSUPPORTED_PLATFORM verbatim.
		return nil, uErr
	}
	//: a non-empty CgroupPath is a Linux concept — reject before spawning.
	if cErr := validateCgroupPath(spec.CgroupPath); cErr != nil {
		//: propagate the typed UNSUPPORTED_PLATFORM verbatim.
		return nil, cErr
	}
	//: build the Job Object confinement from the requested rlimits before the
	//: spawn so an unmappable resource fails fast (UnknownResource), never a child.
	job, jErr := newJobLimit(spec)
	//: propagate an unmappable-resource / job-create failure verbatim.
	if jErr != nil {
		//: the typed UNKNOWN_RESOURCE / RLIMIT_FAILED, no process spawned.
		return nil, jErr
	}
	sio, ioErr := buildStdio(spec)
	//: a stdio fd-setup failure aborts before the spawn.
	if ioErr != nil {
		//: release the (still-empty) job before surfacing the failure.
		job.close()
		//: propagate the typed SPAWN_FAILED from the stdio setup verbatim.
		return nil, ioErr
	}
	p, sErr := spawnWindows(spec, sio)
	//: a CreateProcess failure already left the stdio fds for cleanup.
	if sErr != nil {
		//: release the half-wired stdio fds + the job before surfacing the failure.
		sio.closeAll()
		job.close()
		//: propagate the typed SPAWN_FAILED verbatim.
		return nil, sErr
	}
	//: confine the freshly spawned child; a failed assign kills + reaps it so no
	//: unconfined process escapes (a tiny assign-after-spawn window remains, as
	//: os.StartProcess cannot CREATE_SUSPENDED).
	if aErr := confine(job, p); aErr != nil {
		//: release the stdio fds (no copiers started yet) and surface the failure.
		sio.closeAll()
		//: the typed RLIMIT_FAILED from the assignment.
		return nil, aErr
	}
	//: the child owns its fd dups now — close the parent copies and start copiers.
	sio.afterStart()
	//: hand back the live supervision handle, owning the job for its lifetime.
	return newHandle(p, spec.Setpgid, sio, job), nil
}

// confine assigns p to job (when a job was requested). On failure it kills and
// reaps the child and releases the job, so no unconfined process is left running.
func confine(job *jobLimit, p *os.Process) error {
	//: no job requested — the spawn is intentionally unconfined.
	if job == nil {
		//: nothing to assign.
		return nil
	}
	//: bind the child to the job's limits.
	if aErr := job.assign(p.Pid); aErr != nil {
		//: kill + reap the child so it never runs outside its requested limits.
		swallowErr(p.Kill())
		_, _ = p.Wait()
		//: release the job handle now the spawn is aborted.
		job.close()
		//: surface the typed RLIMIT_FAILED.
		return aErr
	}
	//: the child is confined.
	return nil
}

// spawnWindows builds the os.ProcAttr and starts the process via CreateProcess.
func spawnWindows(spec coreproc.Spec, sio *stdioState) (*os.Process, error) {
	attr := &os.ProcAttr{
		Dir:   spec.Dir,
		Env:   spawnEnv(spec.Env),
		Files: sio.files[:],
		Sys:   &syscall.SysProcAttr{CreationFlags: creationFlags(spec)},
	}
	p, serr := os.StartProcess(spec.Path, buildArgv(spec), attr)
	//: a CreateProcess fault (missing image, access denied) is a typed SPAWN_FAILED.
	if serr != nil {
		//: wrap the StartProcess cause under the central SPAWN_FAILED fields.
		return nil, wrapSpawn(serr, errs.String("path", spec.Path))
	}
	//: the live process the handle supervises.
	return p, nil
}

// spawnEnv honours the Spec.Env contract: a nil Env means an EMPTY environment
// (the known-state guarantee), not the parent's. os.StartProcess inherits on a
// nil Env, so nil is converted to a non-nil empty slice.
func spawnEnv(env []string) []string {
	//: nil Env must spawn with no environment, not inherit the supervisor's.
	if env == nil {
		//: an empty (non-nil) slice gives the child a clean, known environment.
		return []string{}
	}
	//: a caller-supplied slice is used verbatim (pass os.Environ() to inherit).
	return env
}

// creationFlags maps the Spec topology to CreateProcess flags. Setpgid starts a
// new console process group; Setsid has no direct Windows analogue and is ignored.
func creationFlags(spec coreproc.Spec) uint32 {
	var flags uint32
	//: a new process group is the precondition for a later CTRL_BREAK group signal.
	if spec.Setpgid {
		//: lead a fresh console process group.
		flags |= createNewProcessGroup
	}
	//: the assembled CreateProcess creation flags.
	return flags
}

// buildArgv returns the child argv: just [Path] when Args is empty, else Args
// verbatim (Args[0] is argv[0], matching the Unix build).
func buildArgv(spec coreproc.Spec) []string {
	//: an empty Args defaults argv to the program path alone.
	if len(spec.Args) == 0 {
		//: argv[0] is the path when the caller supplied no argv.
		return []string{spec.Path}
	}
	//: a caller-supplied argv is used verbatim, including argv[0].
	return spec.Args
}

// checkUnsupportedSpec rejects the Unix-only Spec fields that this Windows layer
// does not implement, with the uniform UnsupportedPlatform sentinel — resource
// limits are confined via the Job Object cgroup backend, not at spawn here.
func checkUnsupportedSpec(spec coreproc.Spec) error {
	//: dispatch on the first unsupported field so the caller learns it failed.
	//: Rlimits are NOT rejected here — newJobLimit confines the child via a Job
	//: Object (mappable resources) or fails with UnknownResource (unmappable).
	switch {
	//: umask is a Unix file-mode concept with no Windows equivalent.
	case spec.Umask != nil:
		//: degrade honestly rather than ignore the requested umask.
		return coreproc.UnsupportedPlatform
	//: nice / oom_score_adj are Unix scheduler/OOM knobs.
	case spec.Nice != nil || spec.OOMScoreAdj != nil:
		//: no Windows analogue at this layer.
		return coreproc.UnsupportedPlatform
	//: POSIX user/group credentials do not map to Windows tokens here.
	case spec.User != "" || spec.Group != "" || len(spec.Groups) > 0:
		//: degrade honestly rather than spawn with the wrong identity.
		return coreproc.UnsupportedPlatform
	//: ExtraFiles (fd inheritance / socket activation) is a Unix fd concept.
	case len(spec.ExtraFiles) > 0:
		//: no contracted extra-handle inheritance on Windows here.
		return coreproc.UnsupportedPlatform
	//: every remaining field is honourable by the basic spawn.
	default:
		//: nothing unsupported — proceed to spawn.
		return nil
	}
}
