// Package exec_test — black-box acceptance tests for the spawn primitive: a
// normal-exit Wait, a group-kill-with-survivor check, and the SIGTERM→SIGKILL
// escalation, plus the typed-error contracts. Unix-only behaviour is gated on a
// /bin/sh probe so the suite degrades cleanly where it cannot run.
package exec_test

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// shPath is the shell used by the spawn tests; an absent /bin/sh skips them.
const shPath = "/bin/sh"

// requireShell skips the calling test when the host is non-Unix or lacks
// /bin/sh, while still leaving the typed-error tests to assert the contract.
func requireShell(t *testing.T) {
	t.Helper()
	//: the spawn behaviour under test is Unix-only.
	if runtime.GOOS == "windows" {
		//: nothing to exercise on a platform that returns UnsupportedPlatform.
		t.Skip("spawn tests require a Unix host")
	}
	//: a missing shell means the fixture commands cannot run.
	if _, err := os.Stat(shPath); err != nil {
		//: skip rather than fail when the container has no /bin/sh.
		t.Skipf("%s not present: %v", shPath, err)
	}
}

// TestWaitNormalExit asserts Wait reports the exact status of a clean exit.
func TestWaitNormalExit(t *testing.T) {
	t.Parallel()
	requireShell(t)

	type exitCase struct {
		name string
		code int
	}
	//: cover a zero exit and a non-zero status; both must round-trip.
	cases := []exitCase{
		{name: "zero", code: 0},
		{name: "nonzero", code: 7},
	}

	runCase := func(t *testing.T, tc exitCase) {
		t.Helper()
		spec := coreproc.Spec{
			Path: shPath,
			Args: []string{"sh", "-c", "exit " + strconv.Itoa(tc.code)},
		}
		p, err := svcexec.Start(context.Background(), spec)
		//: a clean spawn of /bin/sh must not error.
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		//: a normal exit is not a wait4 fault.
		if wErr != nil {
			t.Fatalf("Wait: %v", wErr)
		}
		//: the reported code must equal the shell's exit status.
		if exit.Code != tc.code {
			t.Fatalf("Code = %d, want %d", exit.Code, tc.code)
		}
		//: a normal exit is never marked signalled.
		if exit.Signaled {
			t.Fatalf("Signaled = true, want false for a normal exit")
		}
		//: a zero exit must report Success.
		if tc.code == 0 && !exit.Success() {
			t.Fatalf("Success() = false, want true for exit 0")
		}
	}

	for _, tc := range cases {
		//: subtest per exit code isolates a regression to one status.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestWaitSignaledExit asserts a signalled death is reported as such with the
// terminating signal and a -1 code.
func TestWaitSignaledExit(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "sleep 30"},
		Setpgid: true,
	}
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn is the precondition for the signal test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: deliver an immediate, unignorable kill to the leader.
	if sErr := p.Signal(coreproc.Signal(syscall.SIGKILL)); sErr != nil {
		t.Fatalf("Signal: %v", sErr)
	}
	exit, wErr := p.Wait()
	//: reaping a signalled child is not itself a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: a signalled death must be flagged.
	if !exit.Signaled {
		t.Fatalf("Signaled = false, want true after SIGKILL")
	}
	//: the terminating signal must be the one delivered.
	if exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("Signal = %v, want SIGKILL", exit.Signal)
	}
	//: a signalled exit reports -1, never a normal status code.
	if exit.Code != -1 {
		t.Fatalf("Code = %d, want -1 for a signalled exit", exit.Code)
	}
}

// TestStopGroupNoSurvivor spawns a leader that forks a grandchild and asserts
// Stop leaves no survivor in the process group — the acceptance (a) contract.
func TestStopGroupNoSurvivor(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: the leader backgrounds a long sleep then waits — the grandchild is the
	//: survivor risk a leader-only kill would leave behind.
	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "sleep 30 & echo $! ; wait"},
		Setpgid: true,
	}
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn is the precondition for the group-kill test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pgid := p.PID()

	//: give the shell a moment to fork the backgrounded sleep into the group.
	time.Sleep(150 * time.Millisecond)

	//: Stop must terminate the whole group, grandchild included.
	if sErr := p.Stop(context.Background(), 2*time.Second, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	//: drive the reap of the leader so the group is fully settled.
	if _, wErr := p.Wait(); wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}

	//: after a short settle, the process group must be entirely gone — a
	//: kill(-pgid, 0) probe returns ESRCH when no member survives.
	deadline := time.Now().Add(2 * time.Second)
	//: poll until the group reports gone or the deadline fails the test.
	for {
		err := syscall.Kill(-pgid, 0)
		//: ESRCH means no surviving member — the acceptance (a) success.
		if err == syscall.ESRCH {
			//: group fully reaped; nothing leaked.
			return
		}
		//: past the deadline with a survivor is a hard failure.
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has a survivor (kill probe err=%v)", pgid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStopEscalatesToKill asserts Stop escalates SIGTERM→SIGKILL after grace
// for a child that ignores SIGTERM — the acceptance (b) contract.
func TestStopEscalatesToKill(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: trap-ignore SIGTERM, then sleep; only SIGKILL can take this child down.
	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "trap '' TERM; sleep 30"},
		Setpgid: true,
	}
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn is the precondition for the escalation test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: let the shell install its TERM trap before we signal it.
	time.Sleep(150 * time.Millisecond)

	grace := 300 * time.Millisecond
	start := time.Now()
	//: Stop must return only after escalating to SIGKILL past the grace window.
	if sErr := p.Stop(context.Background(), grace, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	elapsed := time.Since(start)
	//: escalation cannot have happened before the grace window elapsed.
	if elapsed < grace {
		t.Fatalf("Stop returned in %v, before the %v grace — no escalation", elapsed, grace)
	}

	exit, wErr := p.Wait()
	//: reaping the killed child is not a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: the child must have died by signal, not a normal exit.
	if !exit.Signaled {
		t.Fatalf("Signaled = false, want true after SIGKILL escalation")
	}
	//: the terminating signal must be SIGKILL — SIGTERM was ignored.
	if exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("Signal = %v, want SIGKILL after escalation", exit.Signal)
	}
}

// TestStartInvalidSpec asserts an empty Path is rejected with InvalidSpec on
// every platform (no shell needed — the contract holds even where spawn cannot).
func TestStartInvalidSpec(t *testing.T) {
	t.Parallel()

	_, err := svcexec.Start(context.Background(), coreproc.Spec{})
	//: an empty Path must surface the central INVALID_SPEC code.
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Start(empty) err = %v, want CodeInvalidSpec", err)
	}
}

// TestStartUnknownUser asserts a non-existent user surfaces UnknownUser.
func TestStartUnknownUser(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path: shPath,
		Args: []string{"sh", "-c", "true"},
		User: "this-user-does-not-exist-kitsunium",
	}
	_, err := svcexec.Start(context.Background(), spec)
	//: an unresolvable user must surface the central UNKNOWN_USER code.
	if !errs.HasCode(err, coreproc.CodeUnknownUser) {
		t.Fatalf("Start(bad user) err = %v, want CodeUnknownUser", err)
	}
}

// TestStartUnknownGroup asserts a non-existent group surfaces UnknownGroup.
func TestStartUnknownGroup(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path:  shPath,
		Args:  []string{"sh", "-c", "true"},
		Group: "this-group-does-not-exist-kitsunium",
	}
	_, err := svcexec.Start(context.Background(), spec)
	//: an unresolvable group must surface the central UNKNOWN_GROUP code.
	if !errs.HasCode(err, coreproc.CodeUnknownGroup) {
		t.Fatalf("Start(bad group) err = %v, want CodeUnknownGroup", err)
	}
}

// TestStartUnknownResource asserts a resource with no RLIMIT_* mapping surfaces
// UnknownResource before any spawn happens.
func TestStartUnknownResource(t *testing.T) {
	t.Parallel()

	spec := coreproc.Spec{
		Path: shPath,
		//: ResourceNProc has no stdlib RLIMIT_NPROC mapping — unmappable here.
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNProc: {Soft: 64, Hard: 64},
		},
	}
	_, err := svcexec.Start(context.Background(), spec)
	//: an unmapped resource must surface the central UNKNOWN_RESOURCE code.
	if !errs.HasCode(err, coreproc.CodeUnknownResource) {
		t.Fatalf("Start(bad resource) err = %v, want CodeUnknownResource", err)
	}
}

// TestStartRlimitUnhonourable asserts a mappable Rlimit, which stdlib cannot
// apply in-child, surfaces RlimitFailed rather than a silent drop.
func TestStartRlimitUnhonourable(t *testing.T) {
	t.Parallel()

	spec := coreproc.Spec{
		Path: shPath,
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
		},
	}
	_, err := svcexec.Start(context.Background(), spec)
	//: a valid-but-unhonourable rlimit must surface RLIMIT_FAILED, not silence.
	if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Fatalf("Start(rlimit) err = %v, want CodeRlimitFailed", err)
	}
}

// TestStartUmaskUnhonourable asserts a non-nil Umask, which stdlib cannot apply
// in-child, surfaces RlimitFailed rather than a silent drop.
func TestStartUmaskUnhonourable(t *testing.T) {
	t.Parallel()

	mask := new(int)
	*mask = 0o077
	spec := coreproc.Spec{Path: shPath, Umask: mask}
	_, err := svcexec.Start(context.Background(), spec)
	//: a non-nil Umask must surface RLIMIT_FAILED, never be silently ignored.
	if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Fatalf("Start(umask) err = %v, want CodeRlimitFailed", err)
	}
}

// TestStartContextCancelled asserts an already-cancelled context aborts the
// spawn before any OS work.
func TestStartContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svcexec.Start(ctx, coreproc.Spec{Path: shPath})
	//: a cancelled context must surface context.Canceled, not a spawn.
	if err != context.Canceled {
		t.Fatalf("Start(cancelled) err = %v, want context.Canceled", err)
	}
}
