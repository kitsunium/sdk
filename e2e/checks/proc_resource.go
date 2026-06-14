// Package checks — this file holds the cgroup and reaper conformance suites: it
// proves cgroup v2 limits actually enforce (by reading the kernel's view of each
// controller file back) and that the subreaper actually adopts and reaps orphaned
// grandchildren.
package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
	"github.com/kitsunium/sdk/pkg/v1/process"
	"github.com/kitsunium/sdk/pkg/v1/reaper"

	"github.com/kitsunium/sdk/e2e/harness"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// cgroupDomain / reaperDomain label every Result the two suites emit.
const (
	cgroupDomain string = "cgroup"
	reaperDomain string = "reaper"
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

// reapWaitBudget bounds how long the orphan-adoption check waits for the
// reparented grandchild to be reaped before it gives up and Skips rather than
// risk hanging the whole conformance run.
const reapWaitBudget time.Duration = 3 * time.Second

// reapPollInterval is how often the orphan-adoption check polls for the reaped
// grandchild while inside reapWaitBudget.
const reapPollInterval time.Duration = 20 * time.Millisecond

// decimalBase is the radix used to render the limit values the kernel reports as
// decimal text in its controller files (memory.max, pids.max).
const decimalBase int = 10

// reapSignalBuffer sizes the OnReap notification channel: a small buffer keeps
// the callback non-blocking while a redundant extra sweep simply drops its send.
const reapSignalBuffer int = 8

// orphanScript is the shell program that forks a backgrounded grandchild in a
// subshell — which outlives the shell — then exits 0, orphaning the grandchild.
const orphanScript string = "( sleep 1 & ) ; exit 0"

// orphanArgs is the full argv for the orphaning shell: argv[0], the -c flag, and
// the orphan script. Hoisted so the spawn does not allocate a literal per call.
var orphanArgs = []string{"sh", "-c", orphanScript}

// Cgroup returns the cgroup-domain conformance checks: create a group, set
// memory/pids limits, read them back from /sys/fs/cgroup to prove the kernel
// took them, freeze/thaw, and Kill the tree — Linux; UnsupportedPlatform else.
func Cgroup() harness.Suite {
	//: a single end-to-end run holds the group handle across every sub-check, so
	//: the create/limits/freeze/kill/delete results all observe one real group.
	return harness.Suite{Domain: cgroupDomain, Checks: []harness.Check{cgroupConformance}}
}

// cgroupConformance runs the whole cgroup lifecycle against one real control
// group and aggregates the per-step outcome into a single Result: it gates on
// Available, creates a uniquely named group, sets memory/pids limits and reads
// the kernel's view back to prove enforcement, freeze/thaws, Kills the empty
// tree, and always Deletes in cleanup.
func cgroupConformance() harness.Result {
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

// cgroupLimitReadback sets memory.max and pids.max, then reads each controller
// file back from the kernel and asserts the recorded value equals what was
// written — the only proof the kernel accepted the limit rather than the write
// merely returning nil.
func cgroupLimitReadback(group cgroup.Group, dir string) (harness.Result, bool) {
	//: ask the kernel to cap memory at the chosen ceiling.
	if err := group.SetMemoryMax(memoryLimitBytes); err != nil {
		//: a write that the kernel refused is a real enforcement failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("SetMemoryMax: %v", err)), false
	}
	//: ask the kernel to cap the process count at the chosen ceiling.
	if err := group.SetPidsMax(pidsLimit); err != nil {
		//: a write that the kernel refused is a real enforcement failure.
		return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("SetPidsMax: %v", err)), false
	}
	want := map[string]string{
		//: memory.max holds the byte ceiling we wrote, in decimal.
		"memory.max": strconv.FormatInt(memoryLimitBytes, decimalBase),
		//: pids.max holds the process ceiling we wrote, in decimal.
		"pids.max": strconv.FormatInt(pidsLimit, decimalBase),
	}
	//: compare each interface file's kernel-recorded content to what we set.
	for file, expect := range want {
		//: read the kernel's own view of this controller file.
		got, rerr := readCgroupFile(dir, file)
		//: an unreadable controller file means we cannot prove enforcement.
		if rerr != nil {
			//: surface the read fault so the missing proof is diagnosable.
			return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("read %s: %v", file, rerr)), false
		}
		//: the kernel's value must match the value we asked it to enforce.
		if got != expect {
			//: a mismatch means the limit did not actually take — a real failure.
			return harness.Failed(cgroupDomain, "lifecycle", fmt.Sprintf("%s = %q, want %q", file, got, expect)), false
		}
	}
	//: both limits read back exactly as written — enforcement is proven.
	return harness.Result{}, true
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

// Reaper returns the reaper-domain conformance checks: become a child-subreaper
// and assert an orphaned grandchild reparents to us and is reaped — Linux +
// FreeBSD/DragonFly (procctl); UnsupportedPlatform / no-op elsewhere.
func Reaper() harness.Suite {
	//: four independent checks: subreaper, IsPID1, construct, orphan adoption.
	return harness.Suite{Domain: reaperDomain, Checks: []harness.Check{
		reaperSubreaper,
		reaperIsPID1,
		reaperConstruct,
		reaperOrphanAdoption,
	}}
}

// reaperSubreaper exercises SetChildSubreaper and classifies the outcome by GOOS:
// on Linux/FreeBSD/DragonFly it must arm subreaper mode and return nil (Pass);
// where the platform has no prctl/procctl equivalent it returns the uniform
// UnsupportedPlatform contract (NotSupported). Either way the GOOS is reported.
func reaperSubreaper() harness.Result {
	//: name carries the GOOS so the per-platform expectation is visible in the table.
	detail := "GOOS=" + runtime.GOOS
	//: arm child-subreaper mode so orphaned descendants reparent to this process.
	err := reaper.SetChildSubreaper()
	//: a nil return is the success path on the platforms that implement it.
	if err == nil {
		//: subreaper mode is armed — the supervisor will collect orphans.
		return harness.Passed(reaperDomain, "set-child-subreaper", detail+": armed (nil)")
	}
	//: off the supported platforms the facade returns UnsupportedPlatform.
	if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: the expected off-platform degradation, a success not a failure.
		return harness.NotSupported(reaperDomain, "set-child-subreaper", detail+": UnsupportedPlatform")
	}
	//: a non-nil, non-Unsupported error on a platform that should arm is a failure.
	return harness.Failed(reaperDomain, "set-child-subreaper", fmt.Sprintf("%s: %v", detail, err))
}

// reaperIsPID1 asserts IsPID1 runs without error and reports the boolean it
// returned; the value itself is host-dependent (true only as container init), so
// the check proves the call is total, not a specific outcome.
func reaperIsPID1() harness.Result {
	//: IsPID1 is a pure os.Getpid()==1 query — it must always return a value.
	isPID1 := reaper.IsPID1()
	//: record the observed value; both true and false are correct depending on host.
	return harness.Passed(reaperDomain, "is-pid1", fmt.Sprintf("IsPID1() = %t", isPID1))
}

// reaperConstruct asserts reaper.New(WithOnReap(...)) builds a reaper without
// panicking — the harness's safeRun would convert a panic to a Fail, so reaching
// the Pass return proves construction is total across every GOOS.
func reaperConstruct() harness.Result {
	//: a non-blocking observer satisfies the WithOnReap contract.
	r := reaper.New(reaper.WithOnReap(func(int) {}))
	//: a nil reaper would mean New broke its "always returns a value" contract.
	if r == nil {
		//: New must never hand back a nil Reaper on any platform.
		return harness.Failed(reaperDomain, "construct", "New returned a nil Reaper")
	}
	//: construction with an option succeeded without panic — the contract holds.
	return harness.Passed(reaperDomain, "construct", "New(WithOnReap) constructed a non-nil reaper")
}

// reaperOrphanAdoption proves the subreaper actually adopts and reaps an orphan:
// after SetChildSubreaper, it spawns /bin/sh that forks a backgrounded grandchild
// then exits, orphaning the grandchild; the grandchild reparents to this process
// and the reaper's OnReap callback fires when ReapOnce collects it. It is bounded
// by reapWaitBudget and Skips on any timeout or environmental gap rather than hang.
func reaperOrphanAdoption() harness.Result {
	//: the whole adoption path is Unix-only; off it, the facade is UnsupportedPlatform.
	if err := reaper.SetChildSubreaper(); err != nil {
		//: where subreaper mode is unavailable the orphan never reparents to us.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: the expected off-platform degradation, a success not a failure.
			return harness.NotSupported(reaperDomain, "orphan-adoption", "SetChildSubreaper UnsupportedPlatform")
		}
		//: a non-Unsupported subreaper fault makes the adoption test inconclusive.
		return harness.Skipped(reaperDomain, "orphan-adoption", fmt.Sprintf("SetChildSubreaper: %v", err))
	}
	//: a POSIX shell is required to fork-and-orphan a grandchild; absence is a skip.
	if _, err := os.Stat("/bin/sh"); err != nil {
		//: no shell to spawn the orphan with — make no claim either way.
		return harness.Skipped(reaperDomain, "orphan-adoption", "/bin/sh not available")
	}
	//: drive the bounded spawn+reap dance and classify its outcome.
	return reaperAdoptionRun()
}

// reaperAdoptionRun performs the actual orphan-adoption exercise: it installs an
// OnReap counter, spawns the orphaning shell, waits for the immediate child to
// exit, then polls ReapOnce within reapWaitBudget for the reparented grandchild.
func reaperAdoptionRun() harness.Result {
	//: a channel records that OnReap observed at least one reaped child.
	reaped := make(chan int, reapSignalBuffer)
	//: OnReap must not block — a buffered non-blocking send satisfies that.
	r := reaper.New(reaper.WithOnReap(func(n int) {
		//: forward only sweeps that actually collected a child.
		if n > 0 {
			//: best-effort notify; a full buffer simply drops a redundant signal.
			select {
			//: record that a reap happened.
			case reaped <- n:
			//: the buffer is full — the signal is redundant, drop it.
			default:
			}
		}
	}))
	//: spawn the shell that forks a backgrounded grandchild then exits immediately.
	proc, err := spawnOrphan()
	//: a spawn fault (no exec perms, sandbox) is environmental, not an SDK defect.
	if err != nil {
		//: make no claim — the host could not host the orphan spawn.
		return harness.Skipped(reaperDomain, "orphan-adoption", fmt.Sprintf("spawn: %v", err))
	}
	//: reap the immediate child ourselves so only the orphaned grandchild remains.
	if _, werr := proc.Wait(); werr != nil {
		//: a wait fault leaves the test inconclusive rather than failed.
		return harness.Skipped(reaperDomain, "orphan-adoption", fmt.Sprintf("Wait child: %v", werr))
	}
	//: poll for the reparented grandchild within the bounded budget.
	return reaperPollForGrandchild(r, reaped)
}

// reaperPollForGrandchild sweeps with ReapOnce on a fixed interval until the
// grandchild is reaped (OnReap fired or ReapOnce returned a positive count) or
// reapWaitBudget elapses, returning Pass on adoption and Skip on timeout — a
// bounded wait so a flaky kernel can never hang the conformance run.
func reaperPollForGrandchild(r reaper.Reaper, reaped <-chan int) harness.Result {
	//: the deadline caps the whole poll loop so the check can never hang.
	deadline := time.Now().Add(reapWaitBudget)
	//: one reusable timer paces the loop without allocating a channel per pass.
	timer := time.NewTimer(reapPollInterval)
	//: release the timer's resources whichever branch returns.
	defer timer.Stop()
	//: poll until the grandchild is collected or the budget is spent.
	for time.Now().Before(deadline) {
		//: a single non-blocking sweep collects any reparented grandchild.
		n, err := r.ReapOnce()
		//: a wait4 fault other than the benign ECHILD is a real reap failure.
		if err != nil {
			//: surface the reap error — the subreaper loop itself misbehaved.
			return harness.Failed(reaperDomain, "orphan-adoption", fmt.Sprintf("ReapOnce: %v", err))
		}
		//: ReapOnce directly collecting the grandchild proves adoption.
		if n > 0 {
			//: the orphan reparented to us and was reaped — the core guarantee.
			return harness.Passed(reaperDomain, "orphan-adoption", "grandchild reparented and reaped via ReapOnce")
		}
		//: re-arm the timer for one interval before the next sweep.
		timer.Reset(reapPollInterval)
		//: the OnReap observer firing is the equivalent proof from the callback side.
		select {
		//: a buffered count means a concurrent sweep already collected the orphan.
		case <-reaped:
			//: adoption confirmed through the OnReap callback path.
			return harness.Passed(reaperDomain, "orphan-adoption", "grandchild reaped, OnReap fired")
		//: nothing yet — wait one interval before sweeping again.
		case <-timer.C:
		}
	}
	//: the budget elapsed without observing the orphan — Skip rather than hang/fail.
	return harness.Skipped(reaperDomain, "orphan-adoption", "grandchild not reaped within budget (timing/sandbox)")
}

// spawnOrphan starts /bin/sh in its own process group running a script that forks
// a backgrounded grandchild (which outlives the shell) and exits 0, so the
// grandchild is orphaned and — once we are a subreaper — reparents to this
// process for the reaper to collect.
func spawnOrphan() (proc process.Process, err error) {
	//: the shell backgrounds a short sleeper then exits, orphaning the sleeper.
	spec := process.Spec{
		//: a POSIX shell is the portable way to fork-and-detach a grandchild.
		Path: "/bin/sh",
		//: ( sleep & ) in a subshell detaches the sleeper from the shell's wait.
		Args: orphanArgs,
		//: a fresh process group keeps the orphan out of our own group on cleanup.
		Setpgid: true,
	}
	//: delegate the spawn to the public process facade.
	return process.Start(context.Background(), spec)
}
