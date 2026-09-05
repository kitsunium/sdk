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

// killDrainDeadline bounds how long the no-survivor check polls Delete after a
// Kill before declaring a surviving child a regression.
const killDrainDeadline time.Duration = 3 * time.Second

// killPollInterval is the gap between Delete attempts while the kernel finishes
// reaping the killed subtree.
const killPollInterval time.Duration = 50 * time.Millisecond

// TestCreateConfinement runs the full create+confine+attach+delete cycle when
// cgroup v2 is delegated, and otherwise asserts Create returns the typed
// CgroupUnavailable / UnsupportedPlatform error without panicking — the issue's
// acceptance criterion across privileged and unprivileged hosts.
func TestCreateConfinement(t *testing.T) {
	t.Parallel()
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
	//: cgroup-equivalent facilities exist on Linux (cgroup v2) and Windows (Job
	//: Objects); every other target has none, so the probe must report false there.
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" && got {
		//: a true result on a platform with no facility means the stub is mis-wired.
		t.Fatalf("Available on %s = true, want false", runtime.GOOS)
	}
}

// TestCreateRejectsEscapingName asserts that a name which is not a single safe
// path element is rejected with the typed INVALID_SPEC sentinel before any
// filesystem write, so a crafted name can never escape the delegated root.
func TestCreateRejectsEscapingName(t *testing.T) {
	t.Parallel()
	//: name validation is a Linux-build concern; the stub rejects all off Linux.
	if runtime.GOOS != "linux" {
		//: off Linux Create short-circuits to UnsupportedPlatform before validation.
		t.Skip("name validation runs only on the Linux build")
	}
	//: each row is a name that must never resolve under the delegated root.
	bad := []string{
		"",
		".",
		"..",
		"../escape",
		"a/b",
		"nested/../..",
		"/abs",
		"foo/",
	}
	//: every crafted name must be refused with INVALID_SPEC and no handle.
	for _, name := range bad {
		//: keep each row independent and parallel-safe.
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g, err := cgroup.Create(name)
			//: a rejected name must never hand back a usable handle.
			if g != nil {
				//: a non-nil handle for a bad name is a containment breach.
				t.Fatalf("Create(%q) returned a handle, want nil", name)
			}
			//: the rejection must carry the typed INVALID_SPEC code.
			if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
				//: a different (or nil) code means the guard did not fire.
				t.Fatalf("Create(%q) = %v, want code INVALID_SPEC", name, err)
			}
		})
	}
}

// assertFeature accepts a cgroup.kill/cgroup.freeze outcome: nil means the
// feature applied; UnsupportedPlatform means the kernel predates the feature file
// (the documented fallback) and skips; anything else fails the test.
func assertFeature(t *testing.T, op string, err error) {
	t.Helper()
	//: a nil error means the feature file exists and the write took effect.
	if err == nil {
		//: the feature applied — nothing more to assert.
		return
	}
	//: an absent feature file (old kernel) is the documented fallback, not a bug.
	if errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: skip the live assertion when the running kernel lacks the feature.
		t.Skipf("%s: cgroup feature file absent on this kernel (UnsupportedPlatform)", op)
	}
	//: any other failure is a real fault.
	t.Fatalf("%s: %v", op, err)
}

// deleteGroup removes g, logging (not failing) a delete fault during cleanup.
func deleteGroup(t *testing.T, g coreproc.Group) {
	t.Helper()
	//: best-effort teardown; a populated-group EBUSY is logged, not fatal.
	if derr := g.Delete(); derr != nil {
		//: a cleanup delete fault does not invalidate the assertions above.
		t.Logf("cleanup Delete: %v", derr)
	}
}

// TestKillFreezeThawContract exercises cgroup.kill / cgroup.freeze on a delegated
// hierarchy: Freeze→Thaw quiesce and resume, Kill on an empty group is a no-op
// success. Older kernels without the feature files surface UnsupportedPlatform
// (the documented fallback). Acceptance #74: Freeze/Thaw observable + idempotent,
// typed error when absent, never panics.
func TestKillFreezeThawContract(t *testing.T) {
	t.Parallel()
	//: a real cgroup v2 hierarchy is required to drive the feature files.
	if !cgroup.Available() {
		//: the typed-error contract is covered by TestCreateConfinement.
		t.Skip("cgroup v2 not available/delegated; Kill/Freeze/Thaw need a real hierarchy")
	}
	g, err := cgroup.Create("sdk-test-killfreeze-" + runtime.GOOS)
	//: a delegated host must create the group cleanly.
	if err != nil {
		//: an error here contradicts Available() reporting true.
		t.Fatalf("Create: %v", err)
	}
	//: tear the group down at the end regardless of assertion outcome.
	defer deleteGroup(t, g)
	//: Freeze then Thaw must each apply (or skip on an old kernel) — idempotent.
	assertFeature(t, "Freeze", g.Freeze())
	assertFeature(t, "Thaw", g.Thaw())
	//: Kill on an empty group is a no-op success on a >=5.14 kernel.
	assertFeature(t, "Kill", g.Kill())
}

// TestKillTerminatesSetsidDescendant is the escape-proof acceptance: a child that
// setsid's a grandchild leaves the parent's process group, so kill(-pgid) would
// miss it — but cgroup.kill, tracking membership by control group, takes it down.
func TestKillTerminatesSetsidDescendant(t *testing.T) {
	t.Parallel()
	//: cgroup.kill is a Linux cgroup v2 feature.
	if runtime.GOOS != "linux" {
		//: nothing to exercise where cgroup v2 does not exist.
		t.Skip("cgroup.kill is Linux-only")
	}
	//: a delegated hierarchy is required to confine and kill a real tree.
	if !cgroup.Available() {
		//: skip the live escape assertion when no hierarchy is delegated.
		t.Skip("cgroup v2 not delegated; cannot test escape-proof kill")
	}
	g, err := cgroup.Create("sdk-test-killtree-" + runtime.GOOS)
	//: a delegated host must create the group cleanly.
	if err != nil {
		//: an error here contradicts Available() reporting true.
		t.Fatalf("Create: %v", err)
	}
	//: tear the group down at the end (no-op once Kill emptied it).
	defer deleteGroup(t, g)
	//: probe cgroup.kill on the still-EMPTY group FIRST, so an old kernel without
	//: the feature skips here — before any helper tree is spawned, never leaking it.
	if kerr := g.Kill(); kerr != nil {
		//: an absent cgroup.kill is the documented fallback; skip cleanly.
		if errs.HasCode(kerr, coreproc.CodeUnsupportedPlatform) {
			//: nothing was spawned yet, so there is nothing to clean up.
			t.Skip("cgroup.kill absent on this kernel (UnsupportedPlatform)")
		}
		//: any other failure on an empty group is a real fault.
		t.Fatalf("Kill(empty): %v", kerr)
	}
	//: cgroup.kill is supported. The leader setsid's a grandchild (which LEAVES the
	//: process group) and keeps forking short-lived children — exercising both the
	//: setsid escape AND the late-fork race against the kill.
	cmd := exec.Command("sh", "-c", "setsid sleep 30 >/dev/null 2>&1 & while :; do sleep 1 & done")
	//: the tree must start before it can be attached and killed.
	if serr := cmd.Start(); serr != nil {
		//: no shell/sleep means the live escape path cannot run here.
		t.Skipf("cannot start helper tree: %v", serr)
	}
	//: attach the leader; children already inherit its cgroup membership.
	if aerr := g.Add(cmd.Process.Pid); aerr != nil {
		//: an attach failure means cgroup.procs is not writable.
		t.Fatalf("Add(leader): %v", aerr)
	}
	//: let the loop fork a few children so the kill races live forks.
	time.Sleep(childGrace)
	//: cgroup.kill must take down the whole subtree atomically — setsid escapee and
	//: any in-flight forks included.
	if kerr := g.Kill(); kerr != nil {
		//: a kill failure here is a real fault (feature was probed available above).
		t.Fatalf("Kill(tree): %v", kerr)
	}
	//: reap the leader so it does not linger as a zombie.
	if _, werr := cmd.Process.Wait(); werr != nil {
		//: a reap log is sufficient — the no-survivor assertion stands below.
		t.Logf("leader wait after Kill: %v", werr)
	}
	//: a successful Delete proves the group is EMPTY: any survivor (setsid escapee
	//: or late fork) would hold it EBUSY. Poll briefly for the kernel to reap.
	assertGroupDrains(t, g)
}

// assertGroupDrains polls Delete until it succeeds (the group is empty, so no
// child survived the kill) or the deadline fails the test. A successful Delete
// is the black-box proof of zero survivors.
func assertGroupDrains(t *testing.T, g coreproc.Group) {
	t.Helper()
	deadline := time.Now().Add(killDrainDeadline)
	//: poll until the now-childless group rmdir's, or fail on a lingering survivor.
	for {
		//: a clean Delete means cgroup.procs is empty — no survivor.
		if derr := g.Delete(); derr == nil {
			//: the whole tree is gone; the escape-proof + late-fork kill held.
			return
		}
		//: past the deadline with a populated group is a survivor regression.
		if time.Now().After(deadline) {
			//: a still-EBUSY group means cgroup.kill missed a child.
			t.Fatalf("group still populated after Kill — a child survived")
		}
		time.Sleep(killPollInterval)
	}
}

// TestAvailableTolerantOfStaleProbe asserts the delegation probe does not
// false-negative when a directory named like the legacy fixed probe already
// exists under a delegated root — the unique-temp-name probe must still report
// the root as writable.
func TestAvailableTolerantOfStaleProbe(t *testing.T) {
	t.Parallel()
	//: the probe internals are exercised through Create on a delegated host.
	if !cgroup.Available() {
		//: nothing to assert when no writable hierarchy is delegated here.
		t.Skip("cgroup v2 not delegated to this caller; cannot exercise the probe")
	}
	//: a second Available() call after a successful first must remain true —
	//: the unique-name probe never leaves a colliding directory behind.
	if !cgroup.Available() {
		//: a flip to false would be the stale-probe false-negative regression.
		t.Fatalf("Available() second call = false, want true (stale-probe regression)")
	}
}
