//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sysPidfdSendSignal is __NR_pidfd_send_signal, 424 on every Linux
// architecture of the unified syscall table (Linux 5.1); the stdlib exports no
// number for it on amd64 or arm64, so it is cited here (ADR 0018).
const sysPidfdSendSignal uintptr = 424

// pollInEvent is POLLIN, the event a pidfd reports once its process has exited.
const pollInEvent int16 = 0x1

// pollSlice bounds one ppoll on a pidfd, so a wait for an exit checks its
// context and its timer between slices.
const pollSlice int64 = int64(50 * 1000 * 1000)

// withHandler is what the pidfd helpers need of a process: the handle the
// runtime holds for it, lent for the length of one call (os.Process).
type withHandler interface {
	// WithHandle calls f with the process's pidfd, valid until f returns.
	WithHandle(f func(handle uintptr)) error
}

// pollFd is struct pollfd from poll.h.
type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// Handle returns a duplicate of the pidfd the runtime holds for the child
// (os.Process.WithHandle), close-on-exec, owned by the caller. It reports
// false once the child is reaped — the runtime has released its pidfd — and
// on a kernel that gave the runtime none (Linux < 5.4, or a filter).
func (h *handle) Handle() (*os.File, bool) {
	//: hold off releaseProc while the pidfd is duplicated.
	h.procMu.RLock()
	defer h.procMu.RUnlock()
	//: a reaped child's pidfd is gone with its status.
	if h.leaderReaped() {
		//: nothing to hand out.
		return nil, false
	}
	//: the caller's own descriptor for the same process.
	return dupProcessHandle(h.proc)
}

// SignalHandle delivers sig through the child's pidfd (pidfd_send_signal),
// never by pid: once the child is reaped it fails with SignalFailed wrapping
// ESRCH. A kernel that gave the runtime no pidfd is HandleUnavailable.
func (h *handle) SignalHandle(sig coreproc.Signal) error {
	//: hold off releaseProc for the whole check-then-signal.
	h.procMu.RLock()
	defer h.procMu.RUnlock()
	//: a reaped child is gone: its pid may be another process's by now.
	if h.leaderReaped() {
		//: what the kernel answers a pidfd whose process is gone.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", h.pid), errs.Int("signal", sig.Int()))
	}
	//: the signal goes through the pidfd alone.
	return signalThrough(h.proc, h.pid, sig)
}

// dupProcessHandle duplicates p's pidfd with F_DUPFD_CLOEXEC.
func dupProcessHandle(p withHandler) (*os.File, bool) {
	dup := -1
	hErr := p.WithHandle(func(fd uintptr) {
		r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
		//: a duplicate only when fcntl succeeded.
		if errno == 0 {
			dup = int(r)
		}
	})
	//: no pidfd, a released handle, or a failed duplicate.
	if hErr != nil || dup < 0 {
		//: nothing to hand out.
		return nil, false
	}
	//: the caller owns the duplicate and closes it.
	return os.NewFile(uintptr(dup), "pidfd"), true
}

// signalThrough sends sig through p's pidfd. The runtime's answer for a
// process it has waited is ESRCH; for a kernel without pidfd,
// HandleUnavailable.
func signalThrough(p withHandler, pid int, sig coreproc.Signal) error {
	var errno syscall.Errno
	hErr := p.WithHandle(func(fd uintptr) {
		//: the one signal this call makes.
		errno = pidfdSendSignal(fd, syscall.Signal(sig.Int()))
	})
	//: classify what came back.
	switch {
	case errors.Is(hErr, os.ErrNoHandle):
		//: the runtime holds no pidfd: no race-free path exists.
		return wrapHandleUnavailable(hErr, errs.Int("pid", pid))
	case errors.Is(hErr, os.ErrProcessDone):
		//: the runtime waited the process: it is gone.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", pid), errs.Int("signal", sig.Int()))
	case hErr != nil:
		//: a released handle — nothing is sent by number instead.
		return wrapSignal(hErr, errs.Int("pid", pid), errs.Int("signal", sig.Int()))
	case errno != 0:
		//: the kernel's refusal (ESRCH once the process has exited).
		return wrapSignal(errno, errs.Int("pid", pid), errs.Int("signal", sig.Int()))
	default:
		//: delivered to that process and no other.
		return nil
	}
}

// pidfdSendSignal issues pidfd_send_signal(fd, sig, NULL, 0) and returns its
// errno: zero on delivery, ESRCH, EPERM or EINVAL as the kernel said.
func pidfdSendSignal(fd uintptr, sig syscall.Signal) syscall.Errno {
	_, _, errno := syscall.Syscall6(sysPidfdSendSignal, fd, uintptr(sig), 0, 0, 0, 0)
	//: the kernel's answer.
	return errno
}

// pidfdExited reports whether the process behind fd has exited, waiting up to
// one pollSlice for it (ppoll on POLLIN); a zero wait only reads the state.
func pidfdExited(fd uintptr, wait bool) (bool, error) {
	pfd := pollFd{fd: int32(fd), events: pollInEvent}
	var ts syscall.Timespec
	//: a waiting poll blocks one slice at most.
	if wait {
		ts = syscall.NsecToTimespec(pollSlice)
	}
	n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
	//: EINTR is a slice that ended early.
	if errno != 0 && errno != syscall.EINTR {
		//: a poll fault.
		return false, errno
	}
	//: readable means exited.
	return n == 1 && pfd.revents&pollInEvent != 0, nil
}
