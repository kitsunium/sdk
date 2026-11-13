//go:build darwin || dragonfly || freebsd || netbsd || openbsd

// Package exec — the kqueue handle and the kernel's process record on darwin
// and the BSDs (#729). These run only on those kernels (the e2e-cross lanes:
// macOS, FreeBSD, OpenBSD, NetBSD; DragonFly compiles them only); every
// process they touch is a child the test spawned.
package exec

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxFDProbe bounds the descriptors openFDCount probes.
const maxFDProbe int = 4096

// openFDCount counts this process's open descriptors by fstat(2): /dev/fd
// lists only 0 to 2 on a FreeBSD without fdescfs.
func openFDCount(t *testing.T) int {
	t.Helper()
	n := 0
	var st syscall.Stat_t
	for fd := range maxFDProbe {
		if syscall.Fstat(fd, &st) == nil {
			n++
		}
	}
	return n
}

// TestReadRecordReadsTheKernel pins the record decoded on a real kernel —
// the layout bsdabi holds for this GOOS and GOARCH against the kernel's own
// answer: this process's users are its uid and euid, its start lies in the
// last day, and a child spawned afterwards started no earlier. A pid nobody
// holds is SIGNAL_FAILED (ESRCH).
func TestReadRecordReadsTheKernel(t *testing.T) {
	t.Parallel()
	self, err := readRecord(os.Getpid())
	if err != nil {
		t.Fatalf("readRecord(self) = %v", err)
	}
	if self.ruid != os.Getuid() || self.euid != os.Geteuid() {
		t.Errorf("record users %d/%d, want %d/%d", self.ruid, self.euid, os.Getuid(), os.Geteuid())
	}
	now := time.Now().UnixMicro()
	if self.start <= now-int64(24*time.Hour/time.Microsecond) || self.start > now+int64(time.Minute/time.Microsecond) {
		t.Errorf("record start %d µs, want within the last day of %d", self.start, now)
	}
	child := startRunningSleeper(t)
	got, err := readRecord(child.Pid)
	if err != nil {
		t.Fatalf("readRecord(child) = %v", err)
	}
	if got.start < self.start {
		t.Errorf("the child started at %d µs, before this process (%d)", got.start, self.start)
	}
	if _, err := readRecord(2147483647); !errs.HasCode(err, coreproc.CodeSignalFailed) || !errors.Is(err, syscall.ESRCH) {
		t.Errorf("readRecord(no such pid) = %v, want SIGNAL_FAILED wrapping ESRCH", err)
	}
}

// startRunningSleeper starts /bin/sleep — one execve(2), no shell to exec
// again — and returns once the kernel's record of it carries a start time,
// that is once the child has left execve: os.StartProcess returns as soon as
// execve has closed the child's close-on-exec descriptors, before execve is
// done, and NetBSD answers the record of a process still inside execve with
// no start (fill_kproc2 reads it as a zombie while execve holds p_reflock).
// On NetBSD 10.1 a child signalled in that window was not ended (#1379): in
// one attempt, TestReadRecordReadsTheKernel read its child with start 0 and
// its cleanup's SIGKILL left the sleeper its whole 30 s, while
// TestKnoteFiresAtExit's knote, registered and signalled in the same window,
// never fired in 10 s.
func startRunningSleeper(t *testing.T) *os.Process {
	t.Helper()
	p, err := os.StartProcess("/bin/sleep", []string{"sleep", "30"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("starting the sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Kill()
		_, _ = p.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		record, rErr := readRecord(p.Pid)
		if rErr == nil && record.start != 0 {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sleeper's record never carried a start in 10 s: %+v, %v", record, rErr)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestKnoteFiresAtExit pins the knote: silent while the process runs, fired
// once it has exited — before its parent reaps it. The child has left its
// execve before it is signalled (startRunningSleeper).
func TestKnoteFiresAtExit(t *testing.T) {
	t.Parallel()
	child := startRunningSleeper(t)
	f, raw, err := openKnote(child.Pid)
	if err != nil {
		t.Fatalf("openKnote = %v", err)
	}
	defer func() { _ = f.Close() }()
	if fired, pErr := pollKnote(raw); fired || pErr != nil {
		t.Fatalf("pollKnote while running = %v, %v, want silent", fired, pErr)
	}
	if kErr := child.Signal(syscall.SIGKILL); kErr != nil {
		t.Fatalf("killing the child: %v", kErr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fired, pErr := pollKnote(raw)
		if pErr != nil {
			t.Fatalf("pollKnote = %v", pErr)
		}
		if fired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the knote never fired for the killed, unreaped child")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, kErr := openKnote(2147483647); !errs.HasCode(kErr, coreproc.CodeSignalFailed) {
		t.Errorf("openKnote(no such pid) = %v, want SIGNAL_FAILED", kErr)
	}
}

// TestDeliverRefusesAReusedPID simulates the reuse #729 guards against: a
// handle whose knotes hold a process that has ended, while its pid now names
// another live process — a second child of the test stands in for the
// stranger. Signal must refuse with ESRCH, and the stranger must live.
func TestDeliverRefusesAReusedPID(t *testing.T) {
	t.Parallel()
	first := startOSSleeper(t)
	stranger := startOSSleeper(t)
	a, err := newAdopted(first.Pid)
	if err != nil {
		t.Fatalf("newAdopted = %v", err)
	}
	if kErr := first.Kill(); kErr != nil {
		t.Fatalf("killing the first child: %v", kErr)
	}
	if _, wErr := first.Wait(); wErr != nil {
		t.Fatalf("reaping the first child: %v", wErr)
	}
	//: the pid the handle addresses now belongs to another process.
	a.pid = stranger.Pid
	deadline := time.Now().Add(10 * time.Second)
	for {
		sErr := a.Signal(coreproc.Signal(syscall.SIGKILL))
		if errs.HasCode(sErr, coreproc.CodeSignalFailed) && errors.Is(sErr, syscall.ESRCH) {
			break
		}
		if sErr == nil {
			t.Fatalf("Signal reached the process that took the pid")
		}
		if time.Now().After(deadline) {
			t.Fatalf("Signal = %v, want SIGNAL_FAILED wrapping ESRCH", sErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if kErr := stranger.Signal(syscall.Signal(0)); kErr != nil {
		t.Errorf("the stranger is gone: %v", kErr)
	}
	if _, wErr := a.Wait(); !errs.HasCode(wErr, coreproc.CodeStatusUnknown) {
		t.Errorf("Wait = %v, want STATUS_UNKNOWN", wErr)
	}
}

// TestAttachBoundsOffLinux pins the bounds without a cgroup: a cgroup bound
// refuses before anything is held, and a start time that differs between the
// two reads of the record — the pid changed hands — refuses the adoption.
func TestAttachBoundsOffLinux(t *testing.T) {
	t.Parallel()
	child := startOSSleeper(t)
	if p, err := attachProcess(child.Pid, coreproc.AttachValue{Cgroup: "/"}); p != nil || !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
		t.Errorf("a cgroup bound = %v, %v, want ADOPTION_REFUSED", p, err)
	}
	a, err := newAdopted(child.Pid)
	if err != nil {
		t.Fatalf("newAdopted = %v", err)
	}
	defer a.release()
	own := coreproc.AttachValue{UIDs: []int{os.Getuid(), os.Geteuid()}}
	if err = a.checkUsersHeld(own, procRecord{start: 1}); !errs.HasCode(err, coreproc.CodeSignalFailed) {
		t.Errorf("a start time that changed = %v, want SIGNAL_FAILED", err)
	}
	rec, err := readRecord(child.Pid)
	if err != nil {
		t.Fatalf("readRecord = %v", err)
	}
	if err = a.checkUsersHeld(own, rec); err != nil {
		t.Errorf("the same start time and this user = %v, want nil", err)
	}
	other := coreproc.AttachValue{UIDs: []int{os.Getuid() + 12345}}
	if err = a.checkUsersHeld(other, rec); !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
		t.Errorf("another user = %v, want ADOPTION_REFUSED", err)
	}
}
