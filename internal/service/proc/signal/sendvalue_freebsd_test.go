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

// TestSendValueOnFreeBSD pins #942's sigqueue(2) path: a live child of this
// test is ended by exactly the realtime signal queued, and a reaped one
// answers SIGNAL_FAILED wrapping ESRCH.
func TestSendValueOnFreeBSD(t *testing.T) {
	t.Parallel()
	rt1, err := coreproc.Parse("SIGRTMIN+1")
	//: the range #942 names on FreeBSD.
	if err != nil {
		t.Fatalf("Parse(SIGRTMIN+1): %v", err)
	}
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "sleep 30"}, &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	//: no shell, no child to signal.
	if err != nil {
		t.Skipf("no /bin/sh to spawn: %v", err)
	}
	//: queued to this test's own child.
	if err := signal.SendValue(signal.Target(child.Pid), rt1, 42); err != nil {
		_ = child.Kill()
		_, _ = child.Wait()
		t.Fatalf("SendValue to a live child = %v, want nil", err)
	}
	state, err := child.Wait()
	//: the child is this test's own.
	if err != nil {
		t.Fatalf("waiting for the child: %v", err)
	}
	//: ended by exactly that signal.
	if ws, _ := state.Sys().(syscall.WaitStatus); !ws.Signaled() || int(ws.Signal()) != rt1.Int() {
		t.Fatalf("the child ended %v, want death by signal %d", state, rt1.Int())
	}
	//: reaped: nothing to queue to.
	if err := signal.SendValue(signal.Target(child.Pid), rt1, 42); !errs.HasCode(err, coreproc.CodeSignalFailed) || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SendValue to a reaped child = %v, want SIGNAL_FAILED wrapping ESRCH", err)
	}
}
