//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pidfdNonblock is PIDFD_NONBLOCK (Linux 5.10), O_NONBLOCK's value: the pidfd
// is opened non-blocking, ready for the runtime's poller.
const pidfdNonblock uintptr = syscall.O_NONBLOCK

// pidfdOpener is pidfd_open(2): a descriptor for pid, or the errno.
type pidfdOpener func(pid int, flags uintptr) (int, syscall.Errno)

// adopted is the handle Attach returns: a process this one did not spawn,
// held through a non-blocking pidfd registered with the runtime's poller, so
// its end is observed without a thread parked and without a poll loop.
// Nothing in it addresses the pid by number.
type adopted struct {
	// file is the pidfd; the runtime closes it once the handle is collected.
	file *os.File
	// raw lends the pidfd for one call (Control) or waits on it (Read).
	raw syscall.RawConn
	// done is closed once the process has ended.
	done chan struct{}
	// clk is what Stop's grace window runs on: clock.System from Attach.
	clk clock.Waiter
	// watching starts the one goroutine that observes the end.
	watching sync.Once
	pid      int
}

// attachProcess opens a pollable pidfd on pid, checks want against the
// process it holds, and proves the check concerned that process: it was still
// running once the check was over, so its pid was not yet another's.
func attachProcess(pid int, want coreproc.AttachValue) (coreproc.Process, error) {
	fd, err := openPollablePidfd(pid, rawPidfdOpen)
	//: gone, or no pidfd on this kernel.
	if err != nil {
		//: SIGNAL_FAILED (ESRCH) or HANDLE_UNAVAILABLE.
		return nil, classifyPidfdOpen(err, pid)
	}
	a, err := newAdopted(fd, pid)
	//: the pidfd could not be wrapped.
	if err != nil {
		//: HANDLE_UNAVAILABLE.
		return nil, err
	}
	//: the bounds, read from /proc while the pidfd pins the process.
	if err = checkScope(pid, want); err != nil {
		swallowErr(a.file.Close())
		//: ADOPTION_REFUSED naming the bound.
		return nil, err
	}
	//: ended already: what /proc said may describe a later process.
	if a.exited() {
		swallowErr(a.file.Close())
		//: what a signal through the pidfd would answer.
		return nil, wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: the adopted process.
	return a, nil
}

// rawPidfdOpen is pidfd_open(2) itself (434, ADR 0018).
func rawPidfdOpen(pid int, flags uintptr) (int, syscall.Errno) {
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), flags, 0)
	//: the descriptor, or the kernel's refusal.
	return int(fd), errno
}

// openPollablePidfd opens a non-blocking pidfd on pid: PIDFD_NONBLOCK where
// the kernel has it (5.10), else — EINVAL for the unknown flag — a plain
// pidfd made non-blocking with fcntl, which the poller accepts the same way.
func openPollablePidfd(pid int, open pidfdOpener) (int, error) {
	fd, errno := open(pid, pidfdNonblock)
	//: a kernel before 5.10 refuses the flag, not the pid.
	if errno == syscall.EINVAL {
		fd, errno = open(pid, 0)
		//: then the flag is set on the open file.
		if errno == 0 {
			//: O_NONBLOCK by fcntl.
			if err := syscall.SetNonblock(fd, true); err != nil {
				swallowErr(syscall.Close(fd))
				//: the descriptor could not be made pollable.
				return -1, err
			}
		}
	}
	//: the kernel's refusal.
	if errno != 0 {
		//: ESRCH, ENOSYS, EPERM…
		return -1, errno
	}
	//: a pollable pidfd.
	return fd, nil
}

// classifyPidfdOpen maps pidfd_open's refusal: ESRCH is a process gone
// (SIGNAL_FAILED, as Open says), anything else no handle (HANDLE_UNAVAILABLE).
func classifyPidfdOpen(err error, pid int) error {
	//: no such process.
	if err == syscall.ESRCH {
		//: the verdict a signal through a pidfd would give.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: ENOSYS before Linux 5.3, a filter's EPERM, fcntl's fault.
	return wrapHandleUnavailable(err, errs.Int("pid", pid))
}

// newAdopted wraps a non-blocking pidfd: os.NewFile registers it with the
// runtime's poller, so a read on it parks the goroutine, not a thread.
func newAdopted(fd, pid int) (*adopted, error) {
	f := os.NewFile(uintptr(fd), "pidfd")
	raw, err := f.SyscallConn()
	//: a file the runtime cannot lend.
	if err != nil {
		swallowErr(f.Close())
		//: HANDLE_UNAVAILABLE.
		return nil, wrapHandleUnavailable(err, errs.Int("pid", pid))
	}
	//: the handle; its watcher starts with the first wait.
	return &adopted{file: f, raw: raw, done: make(chan struct{}), clk: clock.System, pid: pid}, nil
}

// deliver is how Signal, SignalGroup and Stop reach the process: through the
// pidfd, never by number.
func (a *adopted) deliver(sig coreproc.Signal) error {
	//: the race-free path is the only path.
	return a.SignalHandle(sig)
}

// SignalHandle delivers sig through the pidfd (pidfd_send_signal); once the
// process has ended it fails with SIGNAL_FAILED wrapping ESRCH.
func (a *adopted) SignalHandle(sig coreproc.Signal) error {
	var errno syscall.Errno
	cErr := a.raw.Control(func(fd uintptr) {
		//: the one signal this call makes.
		errno = pidfdSendSignal(fd, syscall.Signal(sig.Int()))
	})
	//: the handle itself is gone.
	if cErr != nil {
		//: nothing is sent by number instead.
		return wrapSignal(cErr, errs.Int("pid", a.pid), errs.Int("signal", sig.Int()))
	}
	//: the kernel's refusal: ESRCH once the process has ended.
	if errno != 0 {
		//: SIGNAL_FAILED wrapping it.
		return wrapSignal(errno, errs.Int("pid", a.pid), errs.Int("signal", sig.Int()))
	}
	//: delivered to that process and no other.
	return nil
}

// Handle returns a duplicate of the pidfd, close-on-exec, owned by the caller.
// It shares the handle's open file, hence its O_NONBLOCK.
func (a *adopted) Handle() (*os.File, bool) {
	dup := -1
	cErr := a.raw.Control(func(fd uintptr) {
		r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
		//: a duplicate only when fcntl succeeded.
		if errno == 0 {
			dup = int(r)
		}
	})
	//: no duplicate.
	if cErr != nil || dup < 0 {
		//: nothing to hand out.
		return nil, false
	}
	//: the caller's own descriptor for the same process.
	return os.NewFile(uintptr(dup), "pidfd"), true
}

// startWatcher starts the goroutine that closes done once the process has
// ended, and returns once it runs.
func (a *adopted) startWatcher() {
	done := a.done
	running := make(chan struct{})
	//: Goroutine lifecycle: ends when the process ends — the pidfd turns
	//: readable — which a Stop or the process itself brings about, and signals
	//: it by closing done, which every wait selects on. It starts only once a
	//: caller waits, so a handle nobody waits on holds no goroutine.
	go func() {
		close(running)
		defer close(done)
		awaitEnd(a.raw)
	}()
	//: the watcher is running before the first wait parks on done.
	<-running
}

// awaitEnd parks on the pidfd raw lends in the runtime's poller until it
// turns readable — the process has ended. A pidfd the poller refused is
// watched one ppoll slice at a time instead.
func awaitEnd(raw syscall.RawConn) {
	err := raw.Read(func(fd uintptr) bool {
		exited, pErr := pidfdExited(fd, false)
		//: ended, or a fault no wait would mend.
		return exited || pErr != nil
	})
	//: the poller took the wait and saw the end.
	if err == nil {
		return
	}
	//: no poller for this descriptor: bounded slices.
	for {
		var exited bool
		var pErr error
		cErr := raw.Control(func(fd uintptr) {
			//: one slice of the wait.
			exited, pErr = pidfdExited(fd, true)
		})
		//: ended, or the watch cannot go on.
		if exited || pErr != nil || cErr != nil {
			return
		}
	}
}

// exited reports, without waiting, whether the process has ended.
func (a *adopted) exited() bool {
	var exited bool
	var pErr error
	cErr := a.raw.Control(func(fd uintptr) {
		//: a zero-length poll.
		exited, pErr = pidfdExited(fd, false)
	})
	//: a handle that cannot be polled is treated as gone: nothing proves it lives.
	return exited || pErr != nil || cErr != nil
}

// checkScope checks want against the process pid, from /proc: its cgroup v2
// path at or below want.Cgroup, its real and effective uid among want.UIDs.
func checkScope(pid int, want coreproc.AttachValue) error {
	//: the cgroup bound.
	if want.Cgroup != "" {
		cg, err := procCgroup(pid)
		//: no cgroup v2 path, or one outside the bound.
		if err != nil || !withinCgroup(cg, want.Cgroup) {
			//: ADOPTION_REFUSED naming both.
			return wrapAdoptionRefused(err, errs.Int("pid", pid), errs.String("cgroup", cg), errs.String("want", want.Cgroup))
		}
	}
	//: no user bound: nothing more to read.
	if len(want.UIDs) == 0 {
		//: within every bound given.
		return nil
	}
	ruid, euid, err := procUIDs(pid)
	//: the user bound, read from /proc.
	return checkUsers(pid, want, ruid, euid, err)
}

// withinCgroup reports whether the cgroup path cg is want or below it.
func withinCgroup(cg, want string) bool {
	want = filepath.Clean(want)
	//: the root holds every group.
	if want == "/" {
		//: a v2 path is always below the root.
		return strings.HasPrefix(cg, "/")
	}
	//: the group itself, or one of its descendants.
	return cg == want || strings.HasPrefix(cg, want+"/")
}

// procCgroup reads the process's cgroup v2 path: the "0::" line of
// /proc/<pid>/cgroup.
func procCgroup(pid int) (string, error) {
	line, err := procLine("/proc/"+strconv.Itoa(pid)+"/cgroup", "0::")
	//: the path after the hierarchy's id and its empty controller list.
	return line, err
}

// procUIDs reads the process's real and effective uid: the first two fields
// of the Uid: line of /proc/<pid>/status.
func procUIDs(pid int) (ruid, euid int, err error) {
	line, err := procLine("/proc/"+strconv.Itoa(pid)+"/status", "Uid:")
	//: no status.
	if err != nil {
		//: unknown users.
		return -1, -1, err
	}
	fields := strings.Fields(line)
	//: real, effective, saved, filesystem.
	if len(fields) < 2 {
		//: a status line this kernel did not write.
		return -1, -1, syscall.EINVAL
	}
	ruid, rErr := strconv.Atoi(fields[0])
	euid, eErr := strconv.Atoi(fields[1])
	//: both, or neither.
	if rErr != nil || eErr != nil {
		//: unknown users.
		return -1, -1, syscall.EINVAL
	}
	//: the two users.
	return ruid, euid, nil
}

// procLine returns what follows prefix on the first line of the file at path
// that starts with it.
func procLine(path, prefix string) (string, error) {
	f, err := os.Open(path)
	//: the process is gone, or /proc is not mounted.
	if err != nil {
		//: the cause.
		return "", err
	}
	defer func() { swallowErr(f.Close()) }()
	sc := bufio.NewScanner(f)
	//: line by line.
	for sc.Scan() {
		//: the line asked for.
		if rest, ok := strings.CutPrefix(sc.Text(), prefix); ok {
			//: its value.
			return strings.TrimSpace(rest), nil
		}
	}
	//: absent: on cgroup v1 alone, no "0::" line.
	return "", errorOr(sc.Err(), syscall.ENOENT)
}
