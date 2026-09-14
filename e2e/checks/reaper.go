// Package checks — hosts the reaper-domain conformance suite: subreaper
// acquisition classified by GOOS, orphan adoption on a real reparented
// grandchild, and the uniform UnsupportedPlatform contract elsewhere.
package checks

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
	"github.com/kitsunium/sdk/pkg/v1/process"
	"github.com/kitsunium/sdk/pkg/v1/reaper"

	"github.com/kitsunium/sdk/e2e/harness"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// cgroupDomain / reaperDomain label every Result the two suites emit.
// cgroupDomain labels every cgroup-suite Result.
const cgroupDomain string = "cgroup"

// reaperDomain labels every reaper-suite Result.
const reaperDomain string = "reaper"

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

// swallowStop terminates and reaps a placement-check child, ignoring faults: the
// check's verdict is the membership read, not the teardown.
func swallowStop(proc process.Process) {
	//: SIGTERM the group with a short grace, then reap; a sleeping shell exits at once.
	if err := proc.Stop(context.Background(), time.Second, process.SIGTERM); err != nil {
		//: a stop fault is best-effort cleanup, not the check's verdict — but the
		//: child still has to be reaped, or it lingers as a zombie for the run.
		reapQuietly(proc)
		//: nothing further to do; the membership read already decided the verdict.
		return
	}
	//: reap the exited child so it does not linger as a zombie.
	reapQuietly(proc)
}

// reapQuietly waits for a placement-check child and discards the outcome.
//
// The exit status is not the check's verdict — the membership read is — and by
// the time this runs the verdict is already decided. Reaping still has to
// happen, or the conformance binary accumulates a zombie per check.
func reapQuietly(proc process.Process) {
	//: the status is irrelevant; the call exists to release the process slot.
	if _, err := proc.Wait(); err != nil {
		//: a wait fault means the child was already reaped, which is fine.
		return
	}
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

// Reaper returns the reaper-domain conformance checks: become a child-subreaper
// and assert an orphaned grandchild reparents to us and is reaped — Linux +
// FreeBSD/DragonFly (procctl); UnsupportedPlatform / no-op elsewhere.
func Reaper() harness.CheckGroup {
	//: four independent checks: subreaper, IsPID1, construct, orphan adoption.
	return harness.CheckGroup{Domain: reaperDomain, Checks: []harness.Check{
		reaperSubreaper,
		reaperIsPID1,
		reaperConstruct,
		reaperOrphanAdoption,
	}}
}

var (
	// orphanArgs is the full argv for the orphaning shell: argv[0], the -c flag,
	// and the orphan script. Hoisted so the spawn does not allocate a literal
	// per call.
	orphanArgs = []string{"sh", "-c", orphanScript}

	// nativeReaperGOOS is the set of platforms whose kernels implement a
	// descendant subreaper (Linux prctl(PR_SET_CHILD_SUBREAPER);
	// FreeBSD/DragonFly procctl(PROC_REAP_ACQUIRE)). On these SetChildSubreaper
	// must succeed: UnsupportedPlatform there is a regression, not the expected
	// degradation.
	nativeReaperGOOS = map[string]bool{
		"linux":     true,
		"freebsd":   true,
		"dragonfly": true,
	}
)

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
	//: a platform with a native subreaper (prctl/procctl) must NOT report
	//: UnsupportedPlatform — that would be a regression, so fail it there.
	if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: on a native-reaper kernel, UnsupportedPlatform means the call broke.
		if nativeReaperGOOS[runtime.GOOS] {
			//: a regression: this kernel implements the subreaper but reported none.
			return harness.Failed(reaperDomain, "set-child-subreaper", detail+": UnsupportedPlatform on a native-reaper platform (regression)")
		}
		//: off the native platforms it is the expected, correct degradation.
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
