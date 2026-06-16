//go:build windows

// Package exec_test — Windows spawn backend. Exercises the native CreateProcess
// path on the real windows-latest kernel: exit-code decode, stdio capture, the
// honest UnsupportedPlatform degrade for Unix-only Spec fields, the InvalidSpec
// guard, and a group-aware Stop that terminates a long-running child.
package exec_test

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// comspec returns the path to cmd.exe (the always-present Windows shell), or
// skips when it is somehow absent.
func comspec(t *testing.T) string {
	t.Helper()
	cs := os.Getenv("ComSpec")
	//: ComSpec is set on every Windows host; fall back to the canonical path.
	if cs == "" {
		//: the system32 cmd.exe is the documented default.
		cs = `C:\Windows\System32\cmd.exe`
	}
	//: a host without cmd.exe cannot run the spawn checks.
	if _, err := os.Stat(cs); err != nil {
		//: skip rather than fail where the shell is unexpectedly missing.
		t.Skipf("cmd.exe not found: %v", err)
	}
	//: the resolved shell path for the spawn specs.
	return cs
}

// TestWindowsStartExitCode spawns `cmd /c exit 7` and asserts the decoded exit
// code is 7 — the CreateProcess + Wait path on the real kernel.
func TestWindowsStartExitCode(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: comspec(t),
		Args: []string{"cmd", "/c", "exit 7"},
	})
	//: a clean spawn is the precondition for the exit-code assertion.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, werr := p.Wait()
	//: a normal exit must not surface a Wait fault.
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	//: the kernel must report the child's chosen exit code.
	if exit.Code != 7 {
		t.Fatalf("exit Code = %d, want 7", exit.Code)
	}
}

// TestWindowsStdioCapture spawns `cmd /c echo hello` under StdioCapture and
// asserts the output reached the caller's writer.
func TestWindowsStdioCapture(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:   comspec(t),
		Args:   []string{"cmd", "/c", "echo hello"},
		Stdio:  coreproc.StdioCapture,
		Stdout: &out,
	})
	//: a clean spawn is the precondition for the capture assertion.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: Wait joins the copiers, so all output has landed once it returns.
	if _, werr := p.Wait(); werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	//: the captured stdout must carry the child's echo (CRLF tolerated).
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("stdout = %q, want to contain %q", out.String(), "hello")
	}
}

// TestWindowsRlimitsUnsupported asserts a Spec requesting rlimits degrades to the
// uniform sentinel — resource limits are the Job Object cgroup backend's concern,
// not silently dropped at spawn.
func TestWindowsRlimitsUnsupported(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: comspec(t),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			//: any resource triggers the honest degrade on the Windows spawn.
			coreproc.ResourceNoFile: {Soft: 64, Hard: 64},
		},
	})
	//: rlimits at spawn must surface UNSUPPORTED_PLATFORM on Windows.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start with rlimits = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestWindowsInvalidSpec asserts the platform-neutral empty-Path guard fires on
// Windows just as it does on Unix.
func TestWindowsInvalidSpec(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Start(t.Context(), coreproc.Spec{Path: ""})
	//: an empty Path is rejected before any spawn with INVALID_SPEC.
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Start(empty path) = %v, want CodeInvalidSpec", err)
	}
}

// TestWindowsStop spawns a multi-second child and asserts Stop terminates it.
func TestWindowsStop(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: comspec(t),
		//: ping with a count gives a child that lives several seconds.
		Args:  []string{"cmd", "/c", "ping -n 10 127.0.0.1"},
		Stdio: coreproc.StdioNull,
	})
	//: a clean spawn is the precondition for the Stop assertion.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: Stop sends the signal then force-terminates; SIGKILL maps to TerminateProcess.
	if serr := p.Stop(t.Context(), 2*time.Second, coreproc.Signal(syscall.SIGKILL)); serr != nil {
		t.Fatalf("Stop: %v", serr)
	}
}
