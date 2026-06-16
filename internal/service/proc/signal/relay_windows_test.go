//go:build windows

// Package signal_test — Windows Relay backend. Exercises the native delivery
// primitives on the real windows-latest kernel: a reserved target is refused, a
// single-pid relay terminates a live child via TerminateProcess, and a clean
// drain of the source delivers nothing and returns nil.
package signal_test

import (
	"os/exec"
	"syscall"
	"testing"

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
