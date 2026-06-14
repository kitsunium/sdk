// Package cgroup_test — black-box tests for the cgroup service facade.
package cgroup_test

import (
	"os/exec"
	"runtime"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// childGrace is how long the helper sleep child lives — long enough to attach
// and assert, short enough that a leaked child self-reaps quickly.
const childGrace time.Duration = 200 * time.Millisecond

// TestCreateConfinement runs the full create+confine+attach+delete cycle when
// cgroup v2 is delegated, and otherwise asserts Create returns the typed
// CgroupUnavailable / UnsupportedPlatform error without panicking — the issue's
// acceptance criterion across privileged and unprivileged hosts.
func TestCreateConfinement(t *testing.T) {
	//: the unavailable path is the only honest assertion when delegation is absent.
	if !cgroup.Available() {
		//: still prove the typed-error contract before skipping the live path.
		assertUnavailable(t)
		//: document why the live confinement assertion did not run.
		t.Skip("cgroup v2 not available/delegated to this caller; asserted typed-error contract instead")
	}
	//: a delegated host exercises the full lifecycle against a real child.
	runLiveCycle(t)
}

// runLiveCycle creates a group, caps its memory, attaches a short-lived child,
// asserts the attach succeeded, and removes the group after the child exits.
func runLiveCycle(t *testing.T) {
	t.Helper()
	g, err := cgroup.Create("sdk-test-" + runtime.GOOS)
	//: a delegated host must create the group cleanly.
	if err != nil {
		//: an error here contradicts Available() reporting true.
		t.Fatalf("Create on available host: %v", err)
	}
	//: cap memory to prove a controller write reaches the kernel.
	if merr := g.SetMemoryMax(64 << 20); merr != nil {
		//: a write failure means the memory controller is not delegated.
		t.Fatalf("SetMemoryMax: %v", merr)
	}
	child := startChild(t)
	//: attach the child to prove cgroup.procs accepts a foreign pid.
	if aerr := g.Add(child.Process.Pid); aerr != nil {
		//: an attach failure means cgroup.procs is not writable.
		t.Fatalf("Add(child): %v", aerr)
	}
	//: wait for the child to exit so the group becomes empty (rmdir-able).
	if werr := child.Wait(); werr != nil {
		//: a sleep that exits zero should not error on Wait.
		t.Logf("child wait: %v", werr)
	}
	//: the now-empty group must delete cleanly.
	if derr := g.Delete(); derr != nil {
		//: a delete failure means the group was still populated.
		t.Fatalf("Delete: %v", derr)
	}
}

// startChild launches a brief sleep process to serve as the attachment target
// for the confinement assertion.
func startChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", childGrace.String())
	//: the child must start before it can be attached to the group.
	if err := cmd.Start(); err != nil {
		//: no sleep binary means the live attach path cannot run here.
		t.Skipf("cannot start helper child: %v", err)
	}
	//: hand back the running command for attach + reap.
	return cmd
}

// assertUnavailable asserts Create surfaces a typed sentinel — CgroupUnavailable
// on a non-delegated Linux host, UnsupportedPlatform off Linux — and never
// returns a usable handle.
func assertUnavailable(t *testing.T) {
	t.Helper()
	g, err := cgroup.Create("sdk-test-unavail")
	//: an unavailable host must not hand back a live handle.
	if g != nil {
		//: a non-nil handle on the unavailable path is a contract breach.
		t.Fatalf("Create returned a handle on an unavailable host")
	}
	//: pick the platform-correct sentinel to assert.
	want := coreproc.CodeCgroupUnavailable
	//: off Linux the stub returns UNSUPPORTED_PLATFORM instead.
	if runtime.GOOS != "linux" {
		//: the non-Linux stub never reaches the hierarchy check.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the returned error must carry the expected typed code.
	if !errs.HasCode(err, want) {
		//: a missing code breaks the degrade-gracefully contract.
		t.Fatalf("Create on unavailable host = %v, want code %v", err, want)
	}
}

// TestAvailableNeverPanics asserts the delegation probe returns a bool on every
// host without panicking — the no-op robustness contract.
func TestAvailableNeverPanics(t *testing.T) {
	t.Parallel()
	got := cgroup.Available()
	//: off Linux the probe must report false unconditionally.
	if runtime.GOOS != "linux" && got {
		//: a true result off Linux means the stub is mis-wired.
		t.Fatalf("Available off Linux = true, want false")
	}
}
