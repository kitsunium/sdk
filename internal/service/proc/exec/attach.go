package exec

import (
	"context"
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Attach adopts a running process this one did not spawn and holds it so that
// its Wait observes the end and nothing it sends reaches another process that
// reuses the pid: through a pidfd on Linux, through two kqueue EVFILT_PROC
// knotes on darwin and the BSDs, each attached by the kernel to the process
// that had the pid when it was registered and to no later one. Wait and
// WaitContext return once the process has exited, with StatusUnknown and an
// ExitValue whose Code is -1: only a parent reads an exit status, and none is
// promised. Nothing is waited for in the kernel's sense, so a child of this
// process attached here keeps its status for its own handle or the reaper.
// On Linux Signal, SignalGroup and SignalHandle go through the pidfd. On darwin
// and the BSDs no kernel handle signals a process this one did not fork:
// Signal and SignalGroup go by pid once a knote has proven the process still
// running — the instant between that proof and kill(2) is the one a reuse
// could still slip into —, SignalHandle is UnsupportedPlatform and Handle
// reports none. Stop signals, watches for the end within the grace, then kills.
// The handle implements ProcessHandle and ContextWaiter.
//
// A pid below 1 is InvalidSpec; PID 1 and this process are AdoptionRefused; a
// pid that does not exist is SignalFailed wrapping ESRCH; a kernel without
// pidfd_open, or a kqueue refused, is HandleUnavailable; illumos, Solaris and
// Windows are UnsupportedPlatform. ctx is read before any kernel call.
func Attach(ctx context.Context, pid int) (proc coreproc.Process, err error) {
	//: a cancelled context short-circuits before touching the OS.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the cancellation verbatim, as Start reports it.
		return nil, ctxErr
	}
	//: no process has a pid below 1: refused before any kernel call.
	if pid < 1 {
		//: the bare sentinel: a contract violation, no cause.
		return nil, coreproc.InvalidSpec
	}
	//: init and this process are never a supervised service.
	if err := refuseSelfOrInit(pid); err != nil {
		//: ADOPTION_REFUSED naming the pid.
		return nil, err
	}
	//: the platform's adoption, with nothing to bound.
	return attachProcess(pid, coreproc.AttachValue{})
}

// AttachPIDFile reads the PID file at path and adopts the process it names, as
// Attach does. path is absolute and walked one component at a time from the
// root; the file at its end is opened without following a symbolic link,
// without blocking, and must be a regular file holding one decimal PID,
// surrounding blanks allowed. Anything else is PIDFileRefused.
//
// The file is trustworthy when nobody but root and this process's user could
// have made what the walk went through: every directory and the file belong to
// one of them, no directory is writable by its group or others unless sticky,
// the file by neither, and a symbolic link on the way lies in such a directory
// and belongs to one of them. The process a trustworthy file names is adopted;
// from any other file it must satisfy want, and a want that bounds nothing is
// AdoptionRefused. A bound want gives is checked whatever the file, and the
// check is proven to concern the process held, not a later one with its pid.
//
// Off Linux there is no cgroup, so a Cgroup bound is AdoptionRefused; the
// users are read from the kernel's process record (sysctl kinfo_proc), whose
// start time must be the same before and after the knotes are registered.
// Each directory of the walk is held open for reading there, where Linux needs
// only the permission to search it.
func AttachPIDFile(ctx context.Context, path string, want coreproc.AttachValue) (proc coreproc.Process, err error) {
	//: a cancelled context short-circuits before touching the OS.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the cancellation verbatim.
		return nil, ctxErr
	}
	//: the platform's safe read, then the adoption.
	return attachPIDFile(path, want)
}

// refuseSelfOrInit refuses PID 1 and this process: adopting either would let
// a Stop kill the init system or the supervisor itself.
func refuseSelfOrInit(pid int) error {
	//: init, or this very process.
	if pid == 1 || pid == os.Getpid() {
		//: ADOPTION_REFUSED naming the pid.
		return wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.String("why", "PID 1 or this process"))
	}
	//: any other process may be adopted.
	return nil
}

// bounded reports whether want constrains the process at all.
func bounded(want coreproc.AttachValue) bool {
	//: a cgroup or a user set.
	return want.Cgroup != "" || len(want.UIDs) > 0
}
