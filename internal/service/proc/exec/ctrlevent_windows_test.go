//go:build windows

package exec_test

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// ctrlHelperEnv makes the test binary run as a child that exits 3 on a console
// control event (os.Interrupt is how Go reports CTRL_C and CTRL_BREAK).
const ctrlHelperEnv string = "SDK_EXEC_CTRL_HELPER"

// procGetConsoleCP reports 0 when the process has no console (kernel32).
var procGetConsoleCP = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleCP")

// TestWindowsStopDeliversCtrlBreak pins #933: Stop(ctx, 5s, SIGTERM) on a
// Setpgid child that handles CTRL_BREAK returns well inside the grace with the
// child's own exit code 3, not TerminateProcess's. A host without a console
// cannot send a console event at all, and skips.
func TestWindowsStopDeliversCtrlBreak(t *testing.T) {
	//: the helper half: wait for the event, exit 3.
	if os.Getenv(ctrlHelperEnv) != "" {
		got := make(chan os.Signal, 1)
		signal.Notify(got, os.Interrupt)
		if _, err := os.Stdout.WriteString("ready\n"); err != nil {
			os.Exit(1)
		}
		select {
		case <-got:
			os.Exit(3)
		case <-time.After(30 * time.Second):
			os.Exit(1)
		}
	}
	t.Parallel()
	if cp, _, _ := procGetConsoleCP.Call(); cp == 0 {
		t.Skip("this process has no console: a console event cannot be sent")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe = %v", err)
	}
	defer pr.Close()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:    self,
		Args:    []string{self, "-test.run=^TestWindowsStopDeliversCtrlBreak$", "-test.count=1"},
		Env:     append(os.Environ(), ctrlHelperEnv+"=1"),
		Setpgid: true,
		Stdio:   coreproc.StdioCapture,
		Stdout:  pw,
	})
	if err != nil {
		pw.Close()
		t.Fatalf("Start: %v", err)
	}
	ready := make([]byte, 6)
	if _, err := pr.Read(ready); err != nil {
		t.Fatalf("reading the helper's ready line = %v", err)
	}
	start := time.Now()
	if err := p.Stop(t.Context(), 5*time.Second, coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Stop took %v: the grace elapsed, CTRL_BREAK was not delivered", elapsed)
	}
	exit, err := p.Wait()
	pw.Close()
	if err != nil || exit.Code != 3 {
		t.Errorf("exit = (%+v, %v), want the child's own code 3", exit, err)
	}
}

// TestWindowsSignalGroupWithoutAGroup pins the leader-only path the issue keeps
// for a child without Setpgid: SignalGroup(SIGKILL) terminates the leader, as
// it always did.
func TestWindowsSignalGroupWithoutAGroup(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:  comspec(t),
		Args:  []string{"cmd", "/c", "ping -n 30 127.0.0.1"},
		Stdio: coreproc.StdioNull,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.SignalGroup(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("SignalGroup(SIGKILL) = %v", err)
	}
	if exit, err := p.Wait(); err != nil || exit.Code != 1 {
		t.Errorf("exit = (%+v, %v), want TerminateProcess's code 1", exit, err)
	}
}
