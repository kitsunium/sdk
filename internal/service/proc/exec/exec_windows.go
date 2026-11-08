//go:build windows

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

// The CreateProcess priority-class flags (winbase.h) Spec.Nice selects from.
const (
	idlePriorityClass        uint32 = 0x00000040
	belowNormalPriorityClass uint32 = 0x00004000
	normalPriorityClass      uint32 = 0x00000020
	aboveNormalPriorityClass uint32 = 0x00008000
	highPriorityClass        uint32 = 0x00000080
)

// niceHigh and niceIdle bound the Nice ranges that map to HIGH and IDLE: -10
// and below is HIGH, above 10 is IDLE.
const (
	niceHigh int = -10
	niceIdle int = 10
)

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
	//: a bare name is searched in the child's PATH with PATHEXT, as
	//: CreateProcess given an application name does not search at all.
	spec, err = resolveSpec(spec)
	//: not found, or found only through a relative PATH entry.
	if err != nil {
		//: SPAWN_FAILED carrying os/exec's ErrNotFound or ErrDot.
		return nil, err
	}
	//: reject the Unix-only fields with no Windows equivalent before spawning, so
	//: no requested confinement/credential is silently dropped.
	if uErr := checkUnsupportedSpec(spec); uErr != nil {
		//: propagate the uniform UNSUPPORTED_PLATFORM verbatim.
		return nil, uErr
	}
	//: a policy or an I/O class has no Windows path; the zero value passes.
	if schErr := checkScheduling(spec.Scheduling); schErr != nil {
		//: propagate the typed INVALID_SPEC / UNSUPPORTED_PLATFORM verbatim.
		return nil, schErr
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
	//: a confined child is created suspended, so it runs no instruction before
	//: it is in its job — and cannot exit before it, which the assignment would
	//: refuse (#304).
	p, sErr := spawnWindows(spec, sio, job != nil)
	//: a CreateProcess failure already left the stdio fds for cleanup.
	if sErr != nil {
		//: release the half-wired stdio fds + the job before surfacing the failure.
		sio.closeAll()
		job.close()
		//: propagate the typed SPAWN_FAILED verbatim.
		return nil, sErr
	}
	//: confine the suspended child, then resume it; a failed assign or resume
	//: kills + reaps it, so no unconfined process ever runs.
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

// confine assigns p — created suspended — to job (when a job was requested),
// then resumes it. On failure it kills and reaps the child and releases the job,
// so no unconfined process is left running and none ever ran.
func confine(job *jobLimit, p *os.Process) error {
	//: no job requested — the spawn is intentionally unconfined.
	if job == nil {
		//: nothing to assign.
		return nil
	}
	//: bind the suspended child to the job's limits, then let it run.
	aErr := job.assign(p)
	//: only a confined child is resumed.
	if aErr == nil {
		//: the child's first instruction runs under the job's limits.
		aErr = resumeSuspended(p.Pid)
	}
	//: an unconfined or never-resumed child must not survive.
	if aErr != nil {
		//: kill + reap the child so it never runs outside its requested limits.
		swallowErr(p.Kill())
		_, _ = p.Wait()
		//: release the job handle now the spawn is aborted.
		job.close()
		//: surface the typed RLIMIT_FAILED.
		return aErr
	}
	//: the child is confined and running.
	return nil
}

// spawnWindows builds the os.ProcAttr and starts the process via CreateProcess,
// suspended when suspended is set (the child is confined before it runs).
func spawnWindows(spec coreproc.Spec, sio *stdioState, suspended bool) (*os.Process, error) {
	flags := creationFlags(spec)
	//: CREATE_SUSPENDED: the primary thread waits for confine to resume it.
	if suspended {
		//: no instruction of the child runs before its job holds it.
		flags |= createSuspended
	}
	attr := &os.ProcAttr{
		Dir:   spec.Dir,
		Env:   spawnEnv(spec.Env),
		Files: sio.files[:],
		Sys:   &syscall.SysProcAttr{CreationFlags: flags},
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
// (the known-state guarantee), not the parent's, and each variable reaches the
// child once, with its last value. os.StartProcess inherits on a nil Env;
// dedupEnv never returns nil.
func spawnEnv(env []string) []string {
	//: a new slice: an empty one for a nil Env, each name once otherwise.
	return dedupEnv(env)
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
	//: Nice selects the priority class the child is created in, so it binds
	//: from the child's first instruction.
	if spec.Nice != nil {
		flags |= priorityClass(*spec.Nice)
	}
	//: the assembled CreateProcess creation flags.
	return flags
}

// buildArgv returns the child argv: just [coreproc.Spec.Path] when Args is
// empty, else Args verbatim (Args[0] is argv[0], matching the Unix build).
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
	//: oom_score_adj is a Linux OOM-killer knob (Nice maps to a priority class).
	case spec.OOMScoreAdj != nil:
		//: no Windows analogue at this layer.
		return coreproc.UnsupportedPlatform
	//: POSIX user/group credentials do not map to Windows tokens here.
	case spec.User != "" || spec.Group != "" || len(spec.Groups) > 0:
		//: degrade honestly rather than spawn with the wrong identity.
		return coreproc.UnsupportedPlatform
	//: a pseudo-terminal needs ConPTY, which os.StartProcess cannot carry.
	case spec.Stdio == coreproc.StdioPTY:
		//: refused before anything is spawned.
		return coreproc.UnsupportedPlatform
	//: a variable set to the child's PID needs the Unix trampoline.
	case len(spec.PIDEnv) > 0:
		//: refused before anything is spawned.
		return coreproc.UnsupportedPlatform
	//: a sandbox (namespaces, capabilities, Landlock, seccomp) is Linux's:
	//: refused before the spawn, never run unconfined.
	case spec.Sandbox != nil:
		//: refused before anything is spawned.
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

// priorityClass maps a Unix niceness onto a CreateProcess priority class:
// -10 and below HIGH, below 0 ABOVE_NORMAL, 0 NORMAL, 1 to 10 BELOW_NORMAL,
// above 10 IDLE. REALTIME is never chosen: it can starve the system's own
// threads, and no niceness asks for that.
func priorityClass(nice int) uint32 {
	//: from the most favoured class to the least.
	switch {
	case nice <= niceHigh:
		//: a strongly raised priority.
		return highPriorityClass
	case nice < 0:
		//: a raised priority.
		return aboveNormalPriorityClass
	case nice == 0:
		//: the default class, stated.
		return normalPriorityClass
	case nice <= niceIdle:
		//: a lowered priority.
		return belowNormalPriorityClass
	default:
		//: run only when the system is otherwise idle.
		return idlePriorityClass
	}
}
