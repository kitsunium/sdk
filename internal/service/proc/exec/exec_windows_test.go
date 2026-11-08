//go:build windows

// Package exec_test — Windows spawn backend. Exercises the native CreateProcess
// path on the real windows-latest kernel: exit-code decode, stdio capture, the
// honest UnsupportedPlatform degrade for Unix-only Spec fields, the InvalidSpec
// guard, and a group-aware Stop that terminates a long-running child.
package exec_test

import (
	"os"
	"path/filepath"
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

// TestWindowsRlimitMemoryMapped asserts a mappable rlimit (address-space →
// ProcessMemoryLimit) confines the child via a Job Object: the spawn succeeds and
// the child runs to a clean exit under the cap.
func TestWindowsRlimitMemoryMapped(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: comspec(t),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			//: a generous 512 MiB address-space cap the child stays well under.
			coreproc.ResourceAS: {Soft: 512 << 20, Hard: 512 << 20},
		},
	})
	//: a mappable rlimit must confine the child, not reject the spawn.
	if err != nil {
		t.Fatalf("Start with mappable rlimit: %v", err)
	}
	exit, werr := p.Wait()
	//: the confined child must still run to its normal exit.
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	//: the child under the memory cap exits cleanly.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
}

// TestWindowsRlimitUnmappable asserts a Resource with no Job Object analogue
// (RLIMIT_NOFILE) is rejected before any spawn with the typed UnknownResource.
func TestWindowsRlimitUnmappable(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: comspec(t),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			//: NOFILE has no Job Object equivalent — it must be refused.
			coreproc.ResourceNoFile: {Soft: 64, Hard: 64},
		},
	})
	//: an unmappable resource surfaces UNKNOWN_RESOURCE before the spawn.
	if !errs.HasCode(err, coreproc.CodeUnknownResource) {
		t.Fatalf("Start with unmappable rlimit = %v, want CodeUnknownResource", err)
	}
}

// TestWindowsRefusesSandbox is #728's guarantee on Windows: a Spec asking for
// a sandbox is refused with UnsupportedPlatform before anything is spawned, and
// its program never runs.
func TestWindowsRefusesSandbox(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:    comspec(t),
		Args:    []string{"cmd", "/c", "type nul > " + marker},
		Sandbox: &coreproc.SandboxValue{NoNewPrivileges: true},
	})
	//: refused before the spawn.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(Sandbox) = %v, want UNSUPPORTED_PLATFORM", err)
	}
	//: the program never ran.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the refused program ran (marker: %v)", statErr)
	}
	//: LandlockABI says the same.
	if _, abiErr := svcexec.LandlockABI(); !errs.HasCode(abiErr, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("LandlockABI() = %v, want UNSUPPORTED_PLATFORM", abiErr)
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

// TestWindowsDrainGraceBoundsWait spawns a capture child that starts a
// background ping sharing its stdout and exits at once (#1088): the ping holds
// the pipe for seconds, so Wait returns once the default one-second drain
// grace closes, with the child's own exit code 0 and STDIO_DRAIN_TIMEOUT —
// not when the ping ends.
func TestWindowsDrainGraceBoundsWait(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:   comspec(t),
		Args:   []string{"cmd", "/c", "start /b ping -n 8 127.0.0.1 & exit 0"},
		Stdio:  coreproc.StdioCapture,
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	start := time.Now()
	exit, werr := p.Wait()
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Wait took %v: the drain grace did not bound the copier join", elapsed)
	}
	if exit.Code != 0 {
		t.Errorf("exit Code = %d, want the child's own 0", exit.Code)
	}
	if !errs.HasCode(werr, coreproc.CodeStdioDrainTimeout) {
		t.Fatalf("Wait = %v, want STDIO_DRAIN_TIMEOUT", werr)
	}
}

// TestWindowsProcessHandleAndOpen pins #928 on Windows: the Start handle hands
// out a duplicate of its process HANDLE, and Open — exercised on the test's own
// child — terminates through its own HANDLE and refuses Wait.
func TestWindowsProcessHandleAndOpen(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:  comspec(t),
		Args:  []string{"cmd", "/c", "ping -n 30 127.0.0.1"},
		Stdio: coreproc.StdioNull,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = p.Wait() }()
	f, ok := p.(coreproc.ProcessHandle).Handle()
	if !ok {
		t.Fatal("Handle() reported no process HANDLE")
	}
	if err := f.Close(); err != nil {
		t.Errorf("closing the duplicate HANDLE = %v", err)
	}
	opened, err := svcexec.Open(p.PID())
	if err != nil {
		t.Fatalf("Open(%d) = %v", p.PID(), err)
	}
	if _, err := opened.Wait(); !errs.HasCode(err, coreproc.CodeWaitFailed) {
		t.Errorf("Wait on an opened process = %v, want WAIT_FAILED", err)
	}
	if err := opened.Stop(t.Context(), 0, coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("Stop through the opened HANDLE = %v", err)
	}
	if exit, err := p.Wait(); err != nil || exit.Code != 1 {
		t.Errorf("the child's exit = (%+v, %v), want TerminateProcess's code 1", exit, err)
	}
}

// procGetPriorityClass reads a process's priority class (kernel32).
var procGetPriorityClass = syscall.NewLazyDLL("kernel32.dll").NewProc("GetPriorityClass")

// TestWindowsNiceSelectsAPriorityClass pins #940 on Windows: Spec.Nice is no
// longer refused, and 10 creates the child BELOW_NORMAL_PRIORITY_CLASS, read
// back with GetPriorityClass through the child's own HANDLE.
func TestWindowsNiceSelectsAPriorityClass(t *testing.T) {
	t.Parallel()
	const belowNormal uintptr = 0x00004000
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:  comspec(t),
		Args:  []string{"cmd", "/c", "ping -n 5 127.0.0.1"},
		Stdio: coreproc.StdioNull,
		Nice:  new(10),
	})
	if err != nil {
		t.Fatalf("Start(Nice 10) = %v", err)
	}
	defer func() {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
	}()
	f, ok := p.(coreproc.ProcessHandle).Handle()
	if !ok {
		t.Fatal("Handle() reported no process HANDLE")
	}
	defer f.Close()
	class, _, callErr := procGetPriorityClass.Call(f.Fd())
	if class != belowNormal {
		t.Fatalf("GetPriorityClass = %#x (%v), want BELOW_NORMAL_PRIORITY_CLASS", class, callErr)
	}
}

// TestWindowsSchedulingRefused pins that a policy or an I/O class, which have
// no Windows path, are refused before spawning.
func TestWindowsSchedulingRefused(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:       comspec(t),
		Args:       []string{"cmd", "/c", "exit 0"},
		Scheduling: &coreproc.SchedulingValue{Policy: coreproc.SchedIdle},
	})
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(SchedIdle) = %v, want UNSUPPORTED_PLATFORM", err)
	}
}

// TestWindowsPIDEnvRefused pins that Spec.PIDEnv, which needs the Unix
// trampoline, is refused before spawning (#739).
func TestWindowsPIDEnvRefused(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:   comspec(t),
		Args:   []string{"cmd", "/c", "exit 0"},
		PIDEnv: []string{"LISTEN_PID"},
	})
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(PIDEnv) = %v, want UNSUPPORTED_PLATFORM", err)
	}
}
