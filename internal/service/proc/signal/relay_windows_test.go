//go:build windows

// Package signal_test — Windows Relay backend. Exercises the native delivery
// primitives on the real windows-latest kernel: a reserved target is refused, a
// single-pid relay terminates a live child via TerminateProcess, a group relay
// of SIGINT reaches a Go child as os.Interrupt, and a clean drain of the
// source delivers nothing and returns nil.
package signal_test

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// TestRelayRefusesReservedTargetWindows asserts the reserved targets 0 and -1 are
// refused before any delivery, just as on Unix.
func TestRelayRefusesReservedTargetWindows(t *testing.T) {
	t.Parallel()
	//: both reserved targets must be refused with the typed sentinel.
	for _, tgt := range []svcsignal.Target{0, -1} {
		src := make(chan coreproc.Signal, 1)
		//: a queued signal proves the refusal happens before delivery.
		src <- coreproc.Signal(syscall.SIGTERM)
		close(src)
		err := svcsignal.Relay(src, tgt)
		//: a reserved target must surface RELAY_FAILED, never deliver.
		if !errs.HasCode(err, coreproc.CodeRelayFailed) {
			t.Fatalf("Relay(target=%d) = %v, want CodeRelayFailed", tgt, err)
		}
	}
}

// TestRelayTerminatesPidWindows spawns a multi-second child and asserts a pid
// relay terminates it via TerminateProcess.
func TestRelayTerminatesPidWindows(t *testing.T) {
	t.Parallel()
	//: ping with a count gives a child that lives several seconds.
	cmd := exec.Command("ping", "-n", "20", "127.0.0.1")
	//: a host without ping cannot run the terminate assertion.
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start child: %v", err)
	}
	//: relay a single terminate to the child's pid, then close the source.
	src := make(chan coreproc.Signal, 1)
	src <- coreproc.Signal(syscall.SIGKILL)
	close(src)
	//: delivering to a live pid must succeed (TerminateProcess).
	if err := svcsignal.Relay(src, svcsignal.Target(cmd.Process.Pid)); err != nil {
		t.Fatalf("Relay to pid = %v, want nil", err)
	}
	//: the terminated child must now report a non-nil Wait (killed, not exit 0).
	if werr := cmd.Wait(); werr == nil {
		t.Fatal("child Wait = nil after relay terminate, want a termination error")
	}
}

// TestRelayCleanDrainWindows asserts a closed empty source delivers nothing and
// returns nil — even for a target never touched.
func TestRelayCleanDrainWindows(t *testing.T) {
	t.Parallel()
	src := make(chan coreproc.Signal)
	//: an immediately-closed source carries no signals to deliver.
	close(src)
	//: a clean drain with no signals returns nil regardless of target.
	if err := svcsignal.Relay(src, svcsignal.Target(1<<30)); err != nil {
		t.Fatalf("Relay(empty src) = %v, want nil", err)
	}
}

// relayHelperEnv makes the test binary a child that exits 3 on os.Interrupt,
// which is how Go reports CTRL_BREAK; it starts nothing.
const relayHelperEnv string = "SDK_SIGNAL_RELAY_HELPER"

// procGetConsoleCP reports 0 when the process has no console (kernel32).
var procGetConsoleCP = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleCP")

// TestRelayInterruptReachesAGroupWindows pins #933 on the Relay: SIGINT to a
// group target is sent as CTRL_BREAK — Windows delivers no CTRL_C to one
// group, GenerateConsoleCtrlEvent accepting it and no process receiving it —
// so a Go child leading its own group reads os.Interrupt and exits with its
// own code. On the base the CTRL_C went nowhere and the child ran out its
// lifetime. A host without a console cannot send a console event, and skips.
func TestRelayInterruptReachesAGroupWindows(t *testing.T) {
	//: the helper half: wait for the event, exit 3; bounded, starts nothing.
	if os.Getenv(relayHelperEnv) != "" {
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
	cmd := exec.Command(self, "-test.run=^TestRelayInterruptReachesAGroupWindows$", "-test.count=1")
	//: the role this process may have inherited is taken out before it is set.
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(strings.ToUpper(kv), relayHelperEnv+"=")
	}), relayHelperEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe = %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	done := make(chan error, 1)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	if line, rerr := bufio.NewReader(stdout).ReadString('\n'); rerr != nil || line != "ready\n" {
		go func() { done <- cmd.Wait() }()
		t.Fatalf("the helper's first line = (%q, %v), want ready", line, rerr)
	}
	go func() { done <- cmd.Wait() }()
	src := make(chan coreproc.Signal, 1)
	src <- coreproc.Signal(syscall.SIGINT)
	close(src)
	if err := svcsignal.Relay(src, svcsignal.Target(-cmd.Process.Pid)); err != nil {
		t.Fatalf("Relay(SIGINT) to the group = %v, want nil", err)
	}
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != 3 {
			t.Errorf("the helper exited %d, want 3: it did not read os.Interrupt", code)
		}
		done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("the helper never read the relayed SIGINT")
	}
}
