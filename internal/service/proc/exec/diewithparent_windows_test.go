//go:build windows

package exec_test

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// dieWithParentHelperEnv makes the test binary run as the helper process of
// TestWindowsDieWithParent instead of as a test suite.
const dieWithParentHelperEnv string = "SDK_EXEC_DIEWITHPARENT_HELPER"

// TestWindowsDieWithParent starts a helper — this test binary again — that
// spawns `ping -n 6` with DieWithParent set, prints the grandchild's pid and
// blocks. The test terminates the helper, its own child, without a Wait in
// the helper: the Job Object's KILL_ON_JOB_CLOSE handle closes with the
// helper, so the grandchild must be gone within a second (#931). It is only
// observed — opened for SYNCHRONIZE and waited on — never signalled.
func TestWindowsDieWithParent(t *testing.T) {
	//: the helper half: spawn the grandchild, report its pid, block.
	if os.Getenv(dieWithParentHelperEnv) != "" {
		runWindowsDieWithParentHelper(t)
		return
	}
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe = %v", err)
	}
	defer pr.Close()
	helper, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:   self,
		Args:   []string{self, "-test.run=^TestWindowsDieWithParent$", "-test.count=1"},
		Env:    append(os.Environ(), dieWithParentHelperEnv+"=1"),
		Stdio:  coreproc.StdioCapture,
		Stdout: pw,
	})
	if err != nil {
		pw.Close()
		t.Fatalf("Start(helper) = %v", err)
	}
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the grandchild's pid = %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("the helper printed %q, want a pid", line)
	}
	//: the supervisor dies abruptly: TerminateProcess, no Wait of its own.
	if err := helper.Signal(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("terminating the helper = %v", err)
	}
	if _, err := helper.Wait(); err != nil {
		t.Logf("Wait(helper) = %v", err)
	}
	pw.Close()
	gc, err := os.FindProcess(pid)
	//: a pid that no longer opens is a process already gone.
	if err != nil {
		return
	}
	gone := make(chan struct{})
	go func() {
		_, _ = gc.Wait()
		close(gone)
	}()
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatalf("the grandchild %d outlived its parent by a second", pid)
	}
}

// runWindowsDieWithParentHelper is the helper's body.
func runWindowsDieWithParentHelper(t *testing.T) {
	t.Helper()
	child, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:          comspec(t),
		Args:          []string{"cmd", "/c", "ping -n 6 127.0.0.1"},
		Stdio:         coreproc.StdioNull,
		DieWithParent: coreproc.Signal(syscall.SIGTERM),
	})
	if err != nil {
		t.Fatalf("Start(grandchild) = %v", err)
	}
	if _, err := os.Stdout.WriteString(strconv.Itoa(child.PID()) + "\n"); err != nil {
		t.Fatalf("writing the pid = %v", err)
	}
	time.Sleep(time.Minute)
}
