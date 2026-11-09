//go:build linux && !mips && !mipsle && !mips64 && !mips64le

// Package exec — the pollable pidfd and the bounds read from /proc (#729).
package exec

import (
	"os"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestCheckScope pins the bounds read from /proc: the cgroup subtree and the
// users, each checked when given.
func TestCheckScope(t *testing.T) {
	t.Parallel()
	self := os.Getpid()
	if err := checkScope(self, coreproc.AttachValue{UIDs: []int{os.Getuid()}}); err != nil {
		t.Errorf("own uid: %v, want nil", err)
	}
	if err := checkScope(self, coreproc.AttachValue{UIDs: []int{os.Getuid() + 12345}}); !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
		t.Errorf("another uid: %v, want ADOPTION_REFUSED", err)
	}
	cg, err := procCgroup(self)
	if err != nil {
		t.Logf("no cgroup v2 path on this host (%v): the cgroup bound refuses", err)
		if err := checkScope(self, coreproc.AttachValue{Cgroup: "/"}); !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
			t.Errorf("cgroup v1 host, bound /: %v, want ADOPTION_REFUSED", err)
		}
		return
	}
	if err := checkScope(self, coreproc.AttachValue{Cgroup: cg}); err != nil {
		t.Errorf("own cgroup %s: %v, want nil", cg, err)
	}
	if err := checkScope(self, coreproc.AttachValue{Cgroup: "/kitsunium-no-such-cgroup"}); !errs.HasCode(err, coreproc.CodeAdoptionRefused) {
		t.Errorf("another cgroup: %v, want ADOPTION_REFUSED", err)
	}
}

// TestWithinCgroup pins the subtree test: the group itself or a descendant,
// never a sibling that shares a prefix.
func TestWithinCgroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		cg, want string
		in       bool
	}
	tests := []tc{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/a", "/a/", true},
		{"/ab", "/a", false},
		{"/a", "/a/b", false},
		{"/a", "/", true},
		{"", "/", false},
	}
	for _, c := range tests {
		if got := withinCgroup(c.cg, c.want); got != c.in {
			t.Errorf("withinCgroup(%q, %q) = %v, want %v", c.cg, c.want, got, c.in)
		}
	}
}

// TestOpenPollablePidfdFallback pins the path for a kernel before 5.10, which
// refuses PIDFD_NONBLOCK with EINVAL: a plain pidfd, made non-blocking by
// fcntl, that the runtime's poller then waits on like the other.
func TestOpenPollablePidfdFallback(t *testing.T) {
	t.Parallel()
	calls := 0
	old := func(pid int, flags uintptr) (int, syscall.Errno) {
		calls++
		if flags != 0 {
			return -1, syscall.EINVAL
		}
		return rawPidfdOpen(pid, 0)
	}
	child := startOSSleeper(t)
	fd, err := openPollablePidfd(child.Pid, old)
	if err != nil {
		t.Fatalf("openPollablePidfd on an old kernel = %v", err)
	}
	if calls != 2 {
		t.Errorf("pidfd_open called %d times, want 2 (the flag refused, then plain)", calls)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_NONBLOCK == 0 {
		t.Fatalf("the fallback pidfd's flags = %#x (%v), want O_NONBLOCK", flags, errno)
	}
	a, err := newAdopted(fd, child.Pid)
	if err != nil {
		t.Fatalf("newAdopted: %v", err)
	}
	if err := child.Kill(); err != nil {
		t.Fatalf("killing the sleeper: %v", err)
	}
	if _, err := a.Wait(); !errs.HasCode(err, coreproc.CodeStatusUnknown) {
		t.Errorf("Wait on the fallback pidfd = %v, want STATUS_UNKNOWN", err)
	}
	if _, err := child.Wait(); err != nil {
		t.Errorf("reaping the sleeper: %v", err)
	}
}

// TestOpenPollablePidfdRefusals pins the classification: ESRCH is a process
// gone, anything else no handle.
func TestOpenPollablePidfdRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		errno syscall.Errno
		want  errs.Code
	}{
		{syscall.ESRCH, coreproc.CodeSignalFailed},
		{syscall.ENOSYS, coreproc.CodeHandleUnavailable},
		{syscall.EPERM, coreproc.CodeHandleUnavailable},
	} {
		_, err := openPollablePidfd(42, func(int, uintptr) (int, syscall.Errno) { return -1, c.errno })
		if got := classifyPidfdOpen(err, 42); !errs.HasCode(got, c.want) {
			t.Errorf("%v classified as %v, want code %v", c.errno, got, c.want)
		}
	}
}

// openFDCount counts this process's open descriptors, from /proc/self/fd.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("reading /proc/self/fd: %v", err)
	}
	return len(entries)
}
