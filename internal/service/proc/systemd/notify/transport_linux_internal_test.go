//go:build linux

// Package notify — what the shared transport tests need of Linux: /proc's
// descriptor count, SCM_CREDENTIALS' size, and the pidfd paths.
package notify

import (
	"os"
	"syscall"
	"testing"
)

// sysPidfdOpenTest is pidfd_open(2)'s number on the unified table.
const sysPidfdOpenTest uintptr = 434

// truncatingFDs is how many descriptors overflow truncatedOOB.
const truncatingFDs int = 10

// truncatedOOB is a control buffer with room for the credentials and two
// descriptors: the kernel installs those, then sets MSG_CTRUNC.
func truncatedOOB() int {
	return syscall.CmsgSpace(syscall.SizeofUcred) + syscall.CmsgSpace(2*fdBytes)
}

// openFDs counts this process's descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("reading /proc/self/fd: %v", err)
	}
	return len(entries)
}

// TestRecvMainPIDFD pins MAINPIDFD=1: the pidfd's process is the main one, its
// PID read from the kernel, not from MAINPID=.
func TestRecvMainPIDFD(t *testing.T) {
	t.Parallel()
	l, path := testListener(t)
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exec sleep 30"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	t.Cleanup(func() { _ = child.Kill(); _, _ = child.Wait() })
	pidfd, _, errno := syscall.Syscall(sysPidfdOpenTest, uintptr(child.Pid), 0, 0)
	if errno != 0 {
		t.Skipf("no pidfd_open on this kernel: %v", errno)
	}
	defer func() { _ = syscall.Close(int(pidfd)) }()
	sendWith(t, path, "MAINPIDFD=1\nMAINPID=99999\n", int(pidfd))
	v, err := l.Recv()
	if err != nil {
		t.Fatalf("Recv = %v", err)
	}
	defer closeSender(v)
	if mainPIDFDOf(v) == nil || v.MainPID != child.Pid {
		t.Fatalf("MainPIDFD %v MainPID %d, want the child's pidfd and %d", mainPIDFDOf(v), v.MainPID, child.Pid)
	}
	_ = mainPIDFDOf(v).Close()
}

// TestPidfdPID pins the fdinfo read: a pidfd names its process, any other
// descriptor nothing.
func TestPidfdPID(t *testing.T) {
	t.Parallel()
	fd, _, errno := syscall.Syscall(sysPidfdOpenTest, uintptr(os.Getpid()), 0, 0)
	if errno != 0 {
		t.Skipf("no pidfd_open: %v", errno)
	}
	defer func() { _ = syscall.Close(int(fd)) }()
	if pid, err := pidfdPID(int(fd)); err != nil || pid != os.Getpid() {
		t.Errorf("pidfdPID(self) = %d, %v", pid, err)
	}
	r, _ := pipes(t, 1)
	if _, err := pidfdPID(int(r[0].Fd())); err == nil {
		t.Errorf("pidfdPID(a pipe) = nil error, want refusal")
	}
}
