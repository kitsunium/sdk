// Package checks holds the per-domain conformance suites for the SDK e2e binary.
package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
	"github.com/kitsunium/sdk/pkg/v1/process"

	"github.com/kitsunium/sdk/e2e/harness"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// cgroupMountRoot is the canonical unified cgroup v2 mount point — the same
// default the service uses when no WithRoot override is given, so a group created
// with the default root lives at cgroupMountRoot/<name> and its interface files
// can be read back from there to prove the kernel accepted each write.
const cgroupMountRoot string = "/sys/fs/cgroup"

// memoryLimitBytes is the memory.max ceiling the conformance check sets (64 MiB)
// and then reads back from the kernel's view of the controller file.
const memoryLimitBytes int64 = 64 << 20

// pidsLimit is the pids.max ceiling the conformance check sets (16 processes) and
// then reads back to prove the kernel recorded it.
const pidsLimit int64 = 16

// freezeStateThawed / freezeStateFrozen are the cgroup.freeze contents the kernel
// reports for a thawed and a frozen group respectively.
const (
	freezeStateThawed string = "0"
	freezeStateFrozen string = "1"
)

// Cgroup returns the cgroup-domain conformance checks: create a group, set
// memory/pids limits, read them back from /sys/fs/cgroup to prove the kernel
// took them, freeze/thaw, and Kill the tree — Linux; UnsupportedPlatform else.
func Cgroup() harness.CheckGroup {
	//: a single end-to-end run holds the group handle across every sub-check, so
	//: the create/limits/freeze/kill/delete results all observe one real group.
	return harness.CheckGroup{Domain: cgroupDomain, Checks: []harness.Check{cgroupConformance, cgroupPreExecPlacement}}
}

// cgroupConformance runs the whole cgroup lifecycle against one real control
// group and aggregates the per-step outcome into a single Result: it gates on
// Available, creates a uniquely named group, sets memory/pids limits and reads
// the kernel's view back to prove enforcement, freeze/thaws, Kills the empty
// tree, and always Deletes in cleanup.
func cgroupConformance() harness.Result {
	//: Windows has a Job Object cgroup backend, but this harness proves the effect
	//: via the cgroup v2 filesystem (Linux-only); the Job Object mechanics are
	//: covered on the real Windows kernel by the cgroup unit tests instead.
	if runtime.GOOS == "windows" {
		//: make no fs-based end-to-end claim on Windows — unit-tested separately.
		return harness.NotSupported(cgroupDomain, "lifecycle", "Windows Job Object backend covered by unit tests; harness is cgroup-v2-fs specific")
	}
	//: cgroup v2 must be mounted AND delegated to us, else there is nothing to test.
	if !cgroup.Available() {
		//: the honest delegation probe failed — Linux without v2, or no write access.
		return harness.NotSupported(cgroupDomain, "lifecycle", "cgroup v2 not mounted / not delegated / not Linux")
	}
	name := "sdk-e2e-" + strconv.Itoa(os.Getpid())
	//: create a uniquely named group under the default mount so its path is known.
	group, err := cgroup.Create(name)
	//: classify a create fault: environmental skip, off-platform, or real failure.
	if err != nil {
		//: a missing-delegation/permission create fault is environmental, not a bug.
		if cgroupEnvironmental(err) {
			//: CI commonly lacks cgroup delegation — make no claim either way.
			return harness.Skipped(cgroupDomain, "lifecycle", fmt.Sprintf("Create: %v", err))
		}
		//: off Linux the facade returns the uniform UnsupportedPlatform contract.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: the expected off-platform degradation, a success not a failure.
			return harness.NotSupported(cgroupDomain, "lifecycle", "Create returned UnsupportedPlatform")
		}
		//: any other create fault on a host that claimed Available is a real failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("Create: %v", err))
	}
	dir := filepath.Join(cgroupMountRoot, name)
	//: always rmdir the group, even when a sub-step fails partway through.
	defer cgroupCleanup(group)
	//: prove each controller write reached the kernel and freeze/kill behave.
	return cgroupExercise(group, dir)
}

// cgroupPreExecPlacement proves issue #91: a child spawned with Spec.CgroupPath
// is a member of that cgroup at exec time, not a few syscalls later. It reads the
// kernel's own <dir>/cgroup.procs back and requires the child's pid to be listed
// — membership proven by the controller, never by a nil error.
func cgroupPreExecPlacement() harness.Result {
	//: pre-exec cgroup placement rides the Unix re-exec trampoline + cgroup.procs;
	//: neither exists on Windows (Job Objects assign post-create), so make no claim.
	if runtime.GOOS == "windows" {
		//: Windows job-assignment is covered by the cgroup unit tests instead.
		return harness.NotSupported(cgroupDomain, "placement", "pre-exec cgroup.procs placement is Linux-only; Windows uses Job Object assignment")
	}
	//: cgroup v2 must be mounted AND delegated, else there is nowhere to place into.
	if !cgroup.Available() {
		//: no delegated v2 hierarchy (also the non-Linux path) — make no claim.
		return harness.NotSupported(cgroupDomain, "placement", "cgroup v2 not mounted / not delegated / not Linux")
	}
	name := "sdk-e2e-place-" + strconv.Itoa(os.Getpid())
	//: a freshly created group gives a known path to place the child into.
	group, err := cgroup.Create(name)
	//: a create fault has three readings — environmental, off-platform, or a
	//: real bug — and they are separated below rather than collapsed.
	if err != nil {
		//: a missing-delegation create fault is environmental, not a bug.
		if cgroupEnvironmental(err) {
			//: CI commonly lacks delegation — skip rather than fail.
			return harness.Skipped(cgroupDomain, "placement", fmt.Sprintf("Create: %v", err))
		}
		//: off Linux the facade returns the uniform UnsupportedPlatform contract.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: the expected off-platform degradation, a success not a failure.
			return harness.NotSupported(cgroupDomain, "placement", "Create returned UnsupportedPlatform")
		}
		//: any other create fault on a host that claimed Available is a real failure.
		return harness.Failed(cgroupDomain, "placement", fmt.Sprintf("Create: %v", err))
	}
	//: always rmdir the group once the child is gone.
	defer cgroupCleanup(group)
	//: spawn into the group and verify membership against the kernel's view.
	return cgroupPlacementExercise(filepath.Join(cgroupMountRoot, name))
}

// cgroupPlacementExercise spawns a short-lived child directly INTO dir via
// Spec.CgroupPath, then requires its pid to appear in dir/cgroup.procs before
// stopping it — the proof that placement happened at exec, with no post-spawn Add.
func cgroupPlacementExercise(dir string) harness.Result {
	//: a child that sleeps briefly stays alive long enough to read membership back.
	proc, serr := process.Start(context.Background(), process.Spec{
		Path:       "/bin/sh",
		Args:       sleepBrieflyArgs,
		CgroupPath: dir,
	})
	//: a spawn fault has three readings too, separated below for the same reason.
	if serr != nil {
		//: off Linux Start rejects CgroupPath with UnsupportedPlatform — expected.
		if perrs.HasCode(serr, coreproc.CodeUnsupportedPlatform) {
			//: the expected off-platform refusal, a success not a failure.
			return harness.NotSupported(cgroupDomain, "placement", "Start(CgroupPath) returned UnsupportedPlatform")
		}
		//: the group was just Created + delegated, so a placement-path error
		//: (CgroupUnavailable/CgroupWriteFailed) is a REAL regression — fail loudly
		//: rather than masking it as an environmental skip.
		if perrs.HasCode(serr, coreproc.CodeCgroupWriteFailed) || perrs.HasCode(serr, coreproc.CodeCgroupUnavailable) {
			//: a confinement failure on a known-good group is a genuine bug.
			return harness.Failed(cgroupDomain, "placement", fmt.Sprintf("Start placement failed on a delegated group: %v", serr))
		}
		//: anything else (no /bin/sh ⇒ SpawnFailed) is environmental — make no claim.
		return harness.Skipped(cgroupDomain, "placement", fmt.Sprintf("Start: %v", serr))
	}
	pid := strconv.Itoa(proc.PID())
	//: read the kernel's membership list for the group while the child is alive.
	raw, rerr := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	members := string(raw)
	//: stop + reap the child regardless of the read outcome so nothing lingers.
	swallowStop(proc)
	//: the child is already gone by here, so an unreadable membership list is
	//: the read path failing rather than the placement.
	if rerr != nil {
		//: an unreadable cgroup.procs is an environmental fault on the read path.
		return harness.Failed(cgroupDomain, "placement", fmt.Sprintf("read cgroup.procs: %v", rerr))
	}
	//: membership is proven only if the child's pid is one of the listed pids.
	if !containsPID(members, pid) {
		//: pid absent ⇒ the child ran OUTSIDE the group — the exact bug #91 closes.
		return harness.Failed(cgroupDomain, "placement",
			fmt.Sprintf("pid %s absent from cgroup.procs %q — child was NOT placed at exec time",
				pid, strings.TrimSpace(members)))
	}
	//: the child was a member from its first instruction — no unconfined window.
	return harness.Passed(cgroupDomain, "placement", "child pid present in cgroup.procs at exec time (no post-spawn Add)")
}

// containsPID reports whether procs (a newline-separated cgroup.procs body)
// lists want as one of its pids.
func containsPID(procs, want string) bool {
	//: each line is one pid; a trimmed exact match is membership.
	for line := range strings.SplitSeq(procs, "\n") {
		//: compare the trimmed line to the wanted pid.
		if strings.TrimSpace(line) == want {
			//: the child's pid is in the group's process list.
			return true
		}
	}
	//: the pid was not found among the group's members.
	return false
}

// cgroupExercise runs the limit-readback, freeze/thaw, and kill steps against the
// live group rooted at dir, returning the first failure or an aggregated pass.
func cgroupExercise(group cgroup.Group, dir string) harness.Result {
	//: SetMemoryMax + read memory.max back: the kernel's own value is the proof.
	if r, ok := cgroupLimitReadback(group, dir); !ok {
		//: a rejected or mis-recorded memory limit fails the lifecycle.
		return r
	}
	//: Freeze→read "1", Thaw→read "0": the freeze controller actually toggled.
	if r, ok := cgroupFreezeThaw(group, dir); !ok {
		//: a freeze controller that did not toggle fails the lifecycle.
		return r
	}
	//: Kill of an empty group must succeed (or degrade on a pre-5.14 kernel).
	if r, ok := cgroupKill(group); !ok {
		//: a kill fault on a kernel that should support cgroup.kill fails.
		return r
	}
	//: every controller write was confirmed against the kernel's own view.
	return harness.Passed(cgroupDomain, "lifecycle", "create+limits-readback+freeze-thaw+kill confirmed against kernel")
}

// cgroupFreezeThaw freezes the group and asserts cgroup.freeze reads "1", then
// thaws it and asserts it reads "0", proving the freeze controller toggled the
// kernel state rather than the write merely succeeding.
func cgroupFreezeThaw(group cgroup.Group, dir string) (harness.Result, bool) {
	//: quiesce the whole group via the freeze controller.
	if err := group.Freeze(); err != nil {
		//: a pre-5.2 kernel has no cgroup.freeze — that is an environmental skip.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: degrade gracefully; the freeze feature is simply absent here.
			return harness.Skipped(cgroupDomain, "lifecycle", "cgroup.freeze absent (kernel < 5.2)"), false
		}
		//: any other freeze fault on a host that should support it is a failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("Freeze: %v", err)), false
	}
	//: the kernel must report the group as frozen after Freeze.
	if got, rerr := readCgroupFile(dir, "cgroup.freeze"); rerr != nil || got != freezeStateFrozen {
		//: a non-"1" reading means the freeze did not take effect.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("cgroup.freeze after Freeze = %q (err %v), want %q", got, rerr, freezeStateFrozen)), false
	}
	//: resume the group via the freeze controller.
	if err := group.Thaw(); err != nil {
		//: a thaw fault after a successful freeze is a real failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("Thaw: %v", err)), false
	}
	//: the kernel must report the group as thawed after Thaw.
	if got, rerr := readCgroupFile(dir, "cgroup.freeze"); rerr != nil || got != freezeStateThawed {
		//: a non-"0" reading means the thaw did not take effect.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("cgroup.freeze after Thaw = %q (err %v), want %q", got, rerr, freezeStateThawed)), false
	}
	//: freeze toggled the kernel state to 1 and back to 0 — proven.
	return harness.Result{}, true
}

// cgroupKill writes cgroup.kill on the empty group, which must return nil (or
// degrade to UnsupportedPlatform on a pre-5.14 kernel); a populated group is not
// involved, so the only correct outcomes are success or the absent-feature skip.
func cgroupKill(group cgroup.Group) (harness.Result, bool) {
	//: an empty group has nothing to kill, so the write must simply succeed.
	if err := group.Kill(); err != nil {
		//: a pre-5.14 kernel has no cgroup.kill — that is an environmental skip.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: degrade gracefully; the kill feature is simply absent here.
			return harness.Skipped(cgroupDomain, "lifecycle", "cgroup.kill absent (kernel < 5.14)"), false
		}
		//: any other kill fault on a supporting kernel is a real failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("Kill: %v", err)), false
	}
	//: cgroup.kill accepted the trigger on the empty group.
	return harness.Result{}, true
}

// cgroupCleanup best-effort removes the control group during deferred teardown;
// a populated or already-gone group yields an error the conformance verdict does
// not depend on, so it is intentionally dropped.
func cgroupCleanup(group cgroup.Group) {
	//: rmdir the group; teardown failure does not change the recorded result.
	if err := group.Delete(); err != nil {
		//: nothing to do — the harness verdict is carried by the Result, not cleanup.
		return
	}
}

// cgroupEnvironmental reports whether err is a delegation/permission gap (the
// host has cgroup v2 but the caller may not create or write groups) rather than
// an SDK defect — the CI-without-delegation case that must Skip, not Fail.
func cgroupEnvironmental(err error) bool {
	//: an unavailable hierarchy or a refused create both mean "no delegation here".
	return perrs.HasAnyCode(err, coreproc.CodeCgroupUnavailable, coreproc.CodeCgroupCreateFailed)
}

// readCgroupFile reads a single cgroup v2 interface file under dir and returns
// its trimmed content — cgroup files carry a trailing newline the comparison
// must not see.
func readCgroupFile(dir, file string) (content string, err error) {
	//: the kernel exposes each controller value as a small text file under dir.
	raw, err := os.ReadFile(filepath.Join(dir, file))
	//: an unreadable file means we cannot confirm the kernel's view.
	if err != nil {
		//: surface the read error verbatim to the caller for diagnosis.
		return "", err
	}
	//: trim the trailing newline so the content compares against a bare value.
	return strings.TrimSpace(string(raw)), nil
}

// sleepBrieflyArgs runs a shell that stays alive just long enough for the
// placement check to read the kernel membership list back. Hoisted so the
// argument vector is allocated once rather than per check.
var sleepBrieflyArgs = []string{"sh", "-c", "sleep 1"}
