//go:build windows

package exec_test

import (
	"bufio"
	"context"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// ctrlHelperEnv makes the test binary run as a child that answers console
// control events (os.Interrupt is how Go reports CTRL_C and CTRL_BREAK): its
// value is how many events end it, "1" or "2"; each event before the last is
// acknowledged on stdout.
const ctrlHelperEnv string = "SDK_EXEC_CTRL_HELPER"

// ctrlHelperLifetime bounds a helper's life whatever the test does.
const ctrlHelperLifetime = 30 * time.Second

// procGetConsoleCP reports 0 when the process has no console (kernel32).
var procGetConsoleCP = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleCP")

// runCtrlHelper is the helper half, when this process is one: it waits for
// the number of events its role names, acknowledging each but the last on
// stdout, then exits 3; past its lifetime it exits 1. It starts nothing, so a
// helper never starts a helper.
func runCtrlHelper() {
	role := os.Getenv(ctrlHelperEnv)
	if role == "" {
		return
	}
	events := 1
	if role == "2" {
		events = 2
	}
	got := make(chan os.Signal, 2)
	signal.Notify(got, os.Interrupt)
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		os.Exit(1)
	}
	deadline := time.After(ctrlHelperLifetime)
	for i := 1; ; i++ {
		select {
		case <-got:
			if i >= events {
				os.Exit(3)
			}
			if _, err := os.Stdout.WriteString("interrupt\n"); err != nil {
				os.Exit(1)
			}
		case <-deadline:
			os.Exit(1)
		}
	}
}

// ctrlHelperEnviron is this process's environment for a helper ended by
// events: the role it may have inherited taken out before it is set.
func ctrlHelperEnviron(events string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(strings.ToUpper(kv), ctrlHelperEnv+"=")
	})
	return append(env, ctrlHelperEnv+"="+events)
}

// startCtrlHelper starts this binary as a Setpgid helper of test, ended by
// events, and returns it with a reader of its stdout past "ready". A host
// without a console cannot send a console event at all, and skips. The
// cleanup kills a helper still running, on a failed test too.
func startCtrlHelper(t *testing.T, test, events string) (coreproc.Process, *bufio.Reader) {
	t.Helper()
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
	t.Cleanup(func() { _ = pr.Close() })
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path:    self,
		Args:    []string{self, "-test.run=^" + test + "$", "-test.count=1"},
		Env:     ctrlHelperEnviron(events),
		Setpgid: true,
		Stdio:   coreproc.StdioCapture,
		Stdout:  pw,
	})
	if err != nil {
		_ = pw.Close()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
		_ = pw.Close()
	})
	out := bufio.NewReader(pr)
	if line, err := out.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("the helper's first line = (%q, %v), want ready", line, err)
	}
	return p, out
}

// TestWindowsStopDeliversCtrlBreak pins #933: Stop(ctx, 5s, SIGTERM) on a
// Setpgid child that handles CTRL_BREAK returns well inside the grace with the
// child's own exit code 3, not TerminateProcess's.
func TestWindowsStopDeliversCtrlBreak(t *testing.T) {
	runCtrlHelper()
	t.Parallel()
	p, _ := startCtrlHelper(t, "TestWindowsStopDeliversCtrlBreak", "1")
	start := time.Now()
	if err := p.Stop(t.Context(), 5*time.Second, coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Stop took %v: the grace elapsed, CTRL_BREAK was not delivered", elapsed)
	}
	exit, err := p.Wait()
	if err != nil || exit.Code != 3 {
		t.Errorf("exit = (%+v, %v), want the child's own code 3", exit, err)
	}
}

// TestWindowsSignalGroupInterruptIsObserved pins #933's SIGINT criterion as
// Windows can keep it: SignalGroup(SIGINT) on a Setpgid child reaches its group
// as CTRL_BREAK — Windows delivers no CTRL_C to one group — which the Go child
// reads as os.Interrupt, the SIGINT a Unix child reads; the child handles it
// and keeps running, and a later Stop ends it gracefully. On the base, CTRL_C
// was sent: GenerateConsoleCtrlEvent accepted it and the child never saw it.
func TestWindowsSignalGroupInterruptIsObserved(t *testing.T) {
	runCtrlHelper()
	t.Parallel()
	p, out := startCtrlHelper(t, "TestWindowsSignalGroupInterruptIsObserved", "2")
	if err := p.SignalGroup(coreproc.Signal(syscall.SIGINT)); err != nil {
		t.Fatalf("SignalGroup(SIGINT) = %v", err)
	}
	heard := make(chan string, 1)
	go func() {
		line, _ := out.ReadString('\n')
		heard <- line
	}()
	select {
	case line := <-heard:
		if line != "interrupt\n" {
			t.Fatalf("the helper said %q, want interrupt", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the helper never observed SignalGroup(SIGINT)")
	}
	//: observed, not terminated: the child is still running.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	waiter, ok := p.(coreproc.ContextWaiter)
	if !ok {
		t.Fatal("the Windows process is not a ContextWaiter")
	}
	if exit, err := waiter.WaitContext(ctx); err == nil {
		t.Fatalf("the child ended (%+v) on SIGINT, want it running", exit)
	}
	//: the second event ends it with its own code, inside the grace.
	start := time.Now()
	if err := p.Stop(t.Context(), 5*time.Second, coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Stop took %v: the grace elapsed", elapsed)
	}
	if exit, err := p.Wait(); err != nil || exit.Code != 3 {
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
