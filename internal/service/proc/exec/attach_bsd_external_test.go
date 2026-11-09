//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec_test

import (
	"errors"
	"os"
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

// TestAttachKqueueHandle pins what the adopted handle offers on darwin and the
// BSDs (#729): no descriptor to hand out and SignalHandle UNSUPPORTED_PLATFORM
// — no kernel handle reaches a process this one did not fork —, while Signal
// reaches the process held and Wait observes its end.
func TestAttachKqueueHandle(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
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
func TestAttachPIDFileUsersBound(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
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
