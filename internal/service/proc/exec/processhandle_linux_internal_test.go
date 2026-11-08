//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stormSpawns is how many children Test_SignalHandle_reapedChildren reaps and
// then signals through their handle: each must be refused with ESRCH, never
// delivered to whichever process holds the pid by then.
const stormSpawns int = 2000

// pollIn waits up to timeout for fd to become readable — a pidfd is, once its
// process has exited — and reports whether it did.
func pollIn(t *testing.T, fd uintptr, timeout time.Duration) bool {
	t.Helper()
	type pollFd struct {
		fd      int32
		events  int16
		revents int16
	}
	const pollInFlag int16 = 0x1
	pfd := pollFd{fd: int32(fd), events: pollInFlag}
	deadline := time.Now().Add(timeout)
	for {
		ts := syscall.NsecToTimespec(int64(max(time.Until(deadline), 0)))
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		//: a signal to this thread (the runtime's preemption) interrupted the wait: resume it.
		if errno == syscall.EINTR && time.Now().Before(deadline) {
			continue
		}
		if errno != 0 {
			t.Fatalf("ppoll = %v", errno)
		}
		return n == 1 && pfd.revents&pollInFlag != 0
	}
}

// Test_Handle_pollableExit pins #928's first acceptance row: a Start handle
// asserts to ProcessHandle, and Handle returns a pidfd that is not readable
// while the child runs and becomes readable as it exits — before any reap.
func Test_Handle_pollableExit(t *testing.T) {
	t.Parallel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe = %v", err)
	}
	defer pw.Close()
	p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "read x"}, Stdio: coreproc.StdioCapture, Stdin: pr})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	ph, ok := p.(coreproc.ProcessHandle)
	if !ok {
		t.Fatalf("%T does not implement ProcessHandle", p)
	}
	f, ok := ph.Handle()
	if !ok {
		t.Skip("this kernel gives the runtime no pidfd (Linux < 5.4 or a filter)")
	}
	defer f.Close()
	if pollIn(t, f.Fd(), 0) {
		t.Fatal("the pidfd is readable while the child still runs")
	}
	pw.Close()
	start := time.Now()
	if !pollIn(t, f.Fd(), 5*time.Second) {
		t.Fatal("the pidfd never became readable after the child's exit")
	}
	t.Logf("the pidfd became readable %v after the child's stdin closed", time.Since(start))
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}

// Test_SignalHandle_reapedChildren pins #928's second row over a storm of
// spawns: once a child is reaped, SignalHandle refuses with SIGNAL_FAILED
// wrapping ESRCH — it never signals a pid by number, so whatever process holds
// the pid by then receives nothing — and a pidfd duplicated before the reap
// answers ESRCH to pidfd_send_signal itself.
func Test_SignalHandle_reapedChildren(t *testing.T) {
	t.Parallel()
	for i := range stormSpawns {
		p, err := Start(t.Context(), coreproc.Spec{Path: "/bin/true"})
		if err != nil {
			t.Fatalf("Start #%d = %v", i, err)
		}
		ph := p.(coreproc.ProcessHandle)
		f, held := ph.Handle()
		if _, err := p.Wait(); err != nil {
			t.Fatalf("Wait #%d = %v", i, err)
		}
		serr := ph.SignalHandle(coreproc.Signal(syscall.SIGTERM))
		if !errs.HasCode(serr, coreproc.CodeSignalFailed) || !errors.Is(serr, syscall.ESRCH) {
			t.Fatalf("SignalHandle on reaped child #%d = %v, want SIGNAL_FAILED wrapping ESRCH", i, serr)
		}
		if !held {
			continue
		}
		if err := pidfdSendSignal(f.Fd(), syscall.SIGTERM); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("pidfd_send_signal through a pidfd of reaped child #%d = %v, want ESRCH", i, err)
		}
		f.Close()
	}
}

// Test_Open pins the foreign handle #928 adds for a process this one did not
// spawn — exercised on a child of the test, the only process it may signal:
// Signal and Stop go through the pidfd, Wait is refused, and a pid that no
// longer exists is SIGNAL_FAILED wrapping ESRCH.
func Test_Open(t *testing.T) {
	t.Parallel()
	child, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exec sleep 30"}})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	t.Cleanup(func() {
		_ = child.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = child.Wait()
	})
	foreign, err := Open(child.PID())
	if err != nil {
		t.Fatalf("Open(%d) = %v", child.PID(), err)
	}
	if foreign.PID() != child.PID() {
		t.Errorf("PID() = %d, want %d", foreign.PID(), child.PID())
	}
	if _, err := foreign.Wait(); !errs.HasCode(err, coreproc.CodeWaitFailed) {
		t.Errorf("Wait on an opened process = %v, want WAIT_FAILED", err)
	}
	if _, ok := foreign.(coreproc.ProcessHandle).Handle(); !ok {
		t.Errorf("Handle() reports no pidfd for an opened process")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := foreign.Stop(ctx, 5*time.Second, coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Stop through the pidfd = %v", err)
	}
	exit, err := child.Wait()
	if err != nil || !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGTERM) {
		t.Fatalf("the child's exit = (%+v, %v), want a death by SIGTERM", exit, err)
	}
	if _, err := Open(child.PID()); !errs.HasCode(err, coreproc.CodeSignalFailed) || !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Open on a reaped pid = %v, want SIGNAL_FAILED wrapping ESRCH", err)
	}
}
