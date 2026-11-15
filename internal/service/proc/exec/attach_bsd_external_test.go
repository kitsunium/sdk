//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec_test

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// cgroupBound is the cgroup bound TestAttachPIDFile tries off Linux: these
// kernels have no cgroup, so any bound on one is ADOPTION_REFUSED — never
// accepted unchecked.
func cgroupBound(t *testing.T) (cgroup string, code errs.Code, skip string) {
	t.Helper()
	return "/", coreproc.CodeAdoptionRefused, ""
}

// startReading starts a child of the test, through the SDK's spawn, that runs
// its program until killed, and returns once that program runs: cat, one
// execve and no shell, reading a FIFO of the test's temporary directory. The
// test opens the FIFO's write end without blocking, which the kernel refuses
// (ENXIO) while no process holds the FIFO for reading, and cat opens it only
// from its own program, so a write end that opens proves cat has left its
// execve(2). Start returns earlier: os.StartProcess sees the end of the
// child's execve at fd_closeexec (NetBSD 10, kern_exec.c), while execve runs
// on until it releases p_reflock, and on NetBSD 10.1 the SIGTERM sent in that
// window left a `sleep 30` its whole 30 s and an exit status of 0 (#1445, as
// #1379 and #1355). The write end stays open until the test's cleanup, so
// cat, blocked in read(2), reads no end of file while the test runs: it ends
// only of a signal, by the signal's default action on every system.
func startReading(t *testing.T) coreproc.Process {
	t.Helper()
	cat, err := osexec.LookPath("cat")
	if err != nil {
		t.Fatalf("LookPath(cat) = %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "never")
	if err = syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo = %v", err)
	}
	p, err := svcexec.Start(t.Context(), coreproc.Spec{Path: cat, Args: []string{"cat", fifo}})
	if err != nil {
		t.Fatalf("Start = %v", spawnDetail(err))
	}
	t.Cleanup(func() {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		w, oErr := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if oErr == nil {
			//: closed once the test is over; the child's cleanup then reaps cat.
			t.Cleanup(func() { _ = w.Close() })
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("cat never opened the FIFO in 10 s: %v", oErr)
		}
	}
}

// TestAttachKqueueHandle pins what the adopted handle offers on darwin and the
// BSDs (#729): no descriptor to hand out and SignalHandle UNSUPPORTED_PLATFORM
// — no kernel handle reaches a process this one did not fork —, while Signal
// reaches the process held and Wait observes its end. The child is adopted and
// signalled once its program runs (startReading): a SIGTERM sent inside its
// execve went without effect on NetBSD 10.1 (#1445), though kill(2) accepted
// it, a fault the adopted handle neither causes nor can see.
func TestAttachKqueueHandle(t *testing.T) {
	t.Parallel()
	child := startReading(t)
	adopted, err := svcexec.Attach(t.Context(), child.PID())
	if err != nil {
		t.Fatalf("Attach = %v", err)
	}
	ph, ok := adopted.(coreproc.ProcessHandle)
	if !ok {
		t.Fatalf("the adopted handle is no ProcessHandle")
	}
	if f, ok := ph.Handle(); ok || f != nil {
		t.Errorf("Handle = %v, %v, want none", f, ok)
	}
	if sErr := ph.SignalHandle(coreproc.Signal(syscall.SIGTERM)); !errs.HasCode(sErr, coreproc.CodeUnsupportedPlatform) {
		t.Errorf("SignalHandle = %v, want UNSUPPORTED_PLATFORM", sErr)
	}
	if sErr := adopted.Signal(coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Signal = %v", sErr)
	}
	exit, wErr := child.Wait()
	if wErr != nil || !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGTERM) {
		t.Fatalf("the child's exit = %+v, %v, want the SIGTERM sent through the adopted handle", exit, wErr)
	}
	if _, wErr = adopted.Wait(); !errors.Is(wErr, coreproc.StatusUnknown) {
		t.Errorf("adopted Wait = %v, want STATUS_UNKNOWN", wErr)
	}
}

// TestAttachPIDFileUsersBound pins the users bound read from the kernel's
// process record (kinfo_proc, kinfo_proc2) on a real kernel: this user's
// child is adopted from an untrustworthy file, another user's bound refuses.
// The child runs its program before it is read (startReading): NetBSD states
// no start time for a process inside its execve (#1452), and the bound
// compares the start read before the knotes with the one read after them.
func TestAttachPIDFileUsersBound(t *testing.T) {
	t.Parallel()
	child := startReading(t)
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(open, "x.pid")
	if err := os.WriteFile(file, []byte(strconv.Itoa(child.PID())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := svcexec.AttachPIDFile(t.Context(), file, coreproc.AttachValue{UIDs: []int{os.Getuid(), os.Geteuid()}})
	if err != nil || p.PID() != child.PID() {
		t.Fatalf("AttachPIDFile(own users) = %v, %v, want the child", p, err)
	}
	other := coreproc.AttachValue{UIDs: []int{os.Getuid() + 12345}}
	if q, oErr := svcexec.AttachPIDFile(t.Context(), file, other); q != nil || !errs.HasCode(oErr, coreproc.CodeAdoptionRefused) {
		t.Errorf("AttachPIDFile(another user) = %v, %v, want ADOPTION_REFUSED", q, oErr)
	}
	if sErr := p.Stop(t.Context(), 5*time.Second, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Errorf("Stop = %v", sErr)
	}
}
