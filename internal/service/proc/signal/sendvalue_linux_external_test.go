//go:build linux

package signal_test

import (
	"errors"
	"os"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/signal"
)

// startSleeper starts this test's own child, a shell sleeping in its own
// process group, so a realtime signal's default action — termination — is the
// child's report of which signal it got.
func startSleeper(t *testing.T) *os.Process {
	t.Helper()
	p, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "sleep 30"}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
	//: no shell, no child to signal.
	if err != nil {
		t.Skipf("no /bin/sh to spawn: %v", err)
	}
	t.Cleanup(func() { _ = p.Kill(); _, _ = p.Wait() })
	return p
}

// endedBy waits for the child and returns the signal that ended it.
func endedBy(t *testing.T, p *os.Process) syscall.Signal {
	t.Helper()
	state, err := p.Wait()
	//: the child is this test's own.
	if err != nil {
		t.Fatalf("waiting for the child: %v", err)
	}
	ws, _ := state.Sys().(syscall.WaitStatus)
	//: a child that exited rather than died names no signal.
	if !ws.Signaled() {
		t.Fatalf("the child exited %d, want a death by signal", ws.ExitStatus())
	}
	return ws.Signal()
}

// TestSendValueQueuesARealtimeSignal pins #942's SendValue on Linux: a live
// child is ended by exactly the realtime signal queued with a value, and a
// reaped child answers SIGNAL_FAILED wrapping ESRCH.
func TestSendValueQueuesARealtimeSignal(t *testing.T) {
	t.Parallel()
	rt1, err := coreproc.Parse("SIGRTMIN+1")
	//: the range #942 names on Linux.
	if err != nil {
		t.Fatalf("Parse(SIGRTMIN+1): %v", err)
	}
	child := startSleeper(t)
	//: queued with a value the child's default action ignores.
	if err := signal.SendValue(signal.Target(child.Pid), rt1, 42); err != nil {
		t.Fatalf("SendValue to a live child = %v, want nil", err)
	}
	//: the child died of exactly that signal.
	if got := endedBy(t, child); int(got) != rt1.Int() {
		t.Fatalf("the child died of signal %d, want %d", int(got), rt1.Int())
	}
	//: the pid is reaped: nothing to queue to.
	if err := signal.SendValue(signal.Target(child.Pid), rt1, 42); !errs.HasCode(err, coreproc.CodeSignalFailed) || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SendValue to a reaped child = %v, want SIGNAL_FAILED wrapping ESRCH", err)
	}
	//: a group is no queue's target.
	if err := signal.SendValue(signal.Target(-child.Pid), rt1, 42); !errs.HasCode(err, coreproc.CodeSignalFailed) {
		t.Fatalf("SendValue to a group = %v, want SIGNAL_FAILED", err)
	}
}

// TestRelayDeliversARealtimeSignalToAGroup pins #942's Relay acceptance: a
// realtime signal relayed to a child's own process group ends it with exactly
// that signal.
func TestRelayDeliversARealtimeSignalToAGroup(t *testing.T) {
	t.Parallel()
	rt1, err := coreproc.Parse("SIGRTMIN+1")
	//: the range #942 names on Linux.
	if err != nil {
		t.Fatalf("Parse(SIGRTMIN+1): %v", err)
	}
	child := startSleeper(t)
	src := make(chan coreproc.Signal, 1)
	src <- rt1
	close(src)
	//: the child's own group, which only it is in.
	if err := signal.Relay(src, signal.Target(-child.Pid)); err != nil {
		t.Fatalf("Relay(SIGRTMIN+1) = %v, want nil", err)
	}
	//: the group's leader died of exactly that signal.
	if got := endedBy(t, child); int(got) != rt1.Int() {
		t.Fatalf("the child died of signal %d, want %d", int(got), rt1.Int())
	}
}
