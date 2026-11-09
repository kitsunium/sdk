//go:build unix

package childwait

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// observedExit is one call of the observer ReapAnyObserved was given.
type observedExit struct {
	exit  coreproc.ExitValue
	owned bool
}

// TestReapAnyObservedReportsEveryCollection pins #1136's observer on the real
// wait4(-1): an unclaimed child exiting 42 is reported once, unowned, with
// Code 42; one killed by SIGKILL is reported Signaled with SIGKILL; a claimed
// child is reported owned AND its claim still receives the same status.
func TestReapAnyObservedReportsEveryCollection(t *testing.T) {
	requireShell(t)
	orphan, err := startShell(42)
	//: the unclaimed child must exist for its report to mean anything.
	if err != nil {
		t.Fatalf("starting the unclaimed child: %v", err)
	}
	killed, err := os.StartProcess(shellPath, []string{"sh", "-c", "sleep 30"},
		&os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	//: a child of this test, killed by this test.
	if err != nil {
		t.Fatalf("starting the child to kill: %v", err)
	}
	//: this test's own child, never another process.
	if err := killed.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing the test's own child: %v", err)
	}
	owned, claim, err := Spawn(func() (*os.Process, error) { return startShell(7) })
	//: a claimed child, as exec spawns them.
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	seen := make(map[int][]observedExit, 3)
	observe := func(pid int, status *StatusValue, isOwned bool) {
		seen[pid] = append(seen[pid], observedExit{exit: status.Exit(), owned: isOwned})
	}
	deadline := time.Now().Add(reapDeadline)
	//: sweep until the three children are reported, or out of patience.
	for len(seen) < 3 && time.Now().Before(deadline) {
		//: SIGKILL again until it is collected: this test's own child, and a
		//: kill landing in the child's exec window can be lost (seen on NetBSD,
		//: where the process then ran its full sleep).
		if len(seen[killed.Pid]) == 0 {
			_ = killed.Signal(syscall.SIGKILL)
		}
		pid, rerr := ReapAnyObserved(observe)
		//: nothing ready yet, or interrupted: sweep again.
		if errors.Is(rerr, syscall.EINTR) || (rerr == nil && pid == 0) {
			time.Sleep(time.Millisecond)
			continue
		}
		//: the children are ours: ECHILD is not expected before the three.
		if rerr != nil {
			t.Fatalf("ReapAnyObserved: %v after %d of 3", rerr, len(seen))
		}
	}
	type want struct {
		pid      int
		code     int
		signaled bool
		signal   coreproc.Signal
		owned    bool
	}
	wants := []want{
		{pid: orphan.Pid, code: 42},
		{pid: killed.Pid, code: -1, signaled: true, signal: coreproc.Signal(syscall.SIGKILL)},
		{pid: owned.Pid, code: 7, owned: true},
	}
	//: exactly one report per child, in the words a Wait would use.
	for _, w := range wants {
		got := seen[w.pid]
		if len(got) != 1 {
			t.Fatalf("pid %d reported %d times, want once", w.pid, len(got))
		}
		e := got[0]
		if e.exit.Code != w.code || e.exit.Signaled != w.signaled || e.exit.Signal != w.signal || e.owned != w.owned {
			t.Errorf("pid %d: %+v owned=%v, want code %d signaled %v signal %v owned %v",
				w.pid, e.exit, e.owned, w.code, w.signaled, w.signal, w.owned)
		}
	}
	status, ok := claim.Collected()
	//: the observer read the status; the owner still has it.
	if !ok || status.Exit().Code != 7 {
		t.Fatalf("the claim holds %+v (ok %v), want the exit 7 the observer saw", status.Exit(), ok)
	}
}
