//go:build unix && !race

package reaper_test

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// collectedRuns is how many collected children the allocation window measures.
const collectedRuns int = 8

// collectDeadline bounds the wait for one released holder to be collected.
const collectDeadline time.Duration = 30 * time.Second

// TestReapOnceAllocatesNothingWithOrWithoutOnExit is #1136's allocation row
// for an idle sweep: it allocates nothing, and wiring WithOnExit keeps it so —
// the observer is bound once at New. Race off: the race runtime's shadow
// allocations would count; the alloc lane runs it (tools/alloc-lane-targets.txt).
func TestReapOnceAllocatesNothingWithOrWithoutOnExit(t *testing.T) {
	reaper.ReapLock()
	defer reaper.ReapUnlock()
	for name, r := range map[string]interface{ ReapOnce() (int, error) }{
		"without an observer": reaper.New(),
		"with WithOnExit":     reaper.New(reaper.WithOnExit(func(reaper.ExitEvent) {})),
	} {
		//: a host that cannot sweep says nothing about allocations.
		if _, err := r.ReapOnce(); err != nil {
			t.Skipf("ReapOnce is not usable here: %v", err)
		}
		got := mallocsOver(100, func() { _, _ = r.ReapOnce() })
		reportWindow(t, name, got)
		//: zero, not merely few.
		if got.own != 0 {
			t.Errorf("100 idle sweeps %s allocated %d times, want 0", name, got.own)
		}
	}
}

// holder is one child of this test that exits once its input ends.
type holder struct {
	// proc is the started process, released at the end of the test.
	proc *os.Process
	// release is the write end of the child's standard input, -1 once closed.
	release int
	// collected is set once a sweep has taken the holder.
	collected bool
}

// holderEnviron is this process's environment for a holder: every role of
// this binary taken out before the holder's is set, so a holder inherits no
// role twice, and none that would make it start a child.
func holderEnviron() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "SDK_REAPER_ROLE_")
	})
	return append(env, reaper.HoldRoleEnv+"=1")
}

// cloexecPipe is a pipe whose two ends no other child inherits: made under
// ForkLock, as os.Pipe does where the kernel has no pipe2.
func cloexecPipe(t *testing.T) (r, w int) {
	t.Helper()
	var p [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe(p[:])
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	return p[0], p[1]
}

// startHolders starts n holders of this binary, each reading a pipe this test
// keeps the write end of. The cleanup releases and collects every holder
// still alive, on a failed test too.
func startHolders(t *testing.T, r interface{ ReapOnce() (int, error) }, n int) []holder {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable to re-run: %v", err)
	}
	holders := make([]holder, 0, n)
	t.Cleanup(func() {
		pending := 0
		for i := range holders {
			if holders[i].release >= 0 {
				_ = syscall.Close(holders[i].release)
			}
			if !holders[i].collected {
				pending++
			}
			_ = holders[i].proc.Release()
		}
		//: every holder left ends on EOF at once; sweep them all up.
		deadline := time.Now().Add(collectDeadline)
		for reaped := 0; reaped < pending && time.Now().Before(deadline); {
			n, err := r.ReapOnce()
			if err != nil {
				return
			}
			reaped += n
			time.Sleep(time.Millisecond)
		}
	})
	for range n {
		rfd, wfd := cloexecPipe(t)
		stdin := os.NewFile(uintptr(rfd), "holder-stdin")
		p, err := os.StartProcess(exe, []string{exe, "-test.run=^$"}, &os.ProcAttr{
			Env:   holderEnviron(),
			Files: []*os.File{stdin, os.Stderr, os.Stderr},
		})
		_ = stdin.Close()
		if err != nil {
			_ = syscall.Close(wfd)
			t.Fatalf("starting a holder: %v", err)
		}
		holders = append(holders, holder{proc: p, release: wfd})
	}
	return holders
}

// TestReapOnceAllocatesNothingPerCollectedChild is #1136's allocation row
// with children to collect: each measured call releases one holder of this
// test, sweeps until the reaper collects it and hands its exit — Usage
// included — to the WithOnExit observer, and the window counts no allocation
// at all. A sweep over nothing would prove nothing about the status a
// collection converts, which is where a *UsageValue was allocated per child
// until ExitValue carried Usage by value.
// Not parallel, and under the reap lock: wait4(-1) collects every child of
// the test binary.
func TestReapOnceAllocatesNothingPerCollectedChild(t *testing.T) {
	reaper.ReapLock()
	defer reaper.ReapUnlock()
	var (
		seen, unowned, exited, withUsage int
		timedOut, sweepFailed            bool
	)
	r := reaper.New(reaper.WithOnExit(func(e reaper.ExitEvent) {
		seen++
		if !e.Owned {
			unowned++
		}
		if e.Exit.Code == reaper.HoldExitCode {
			exited++
		}
		if e.Exit.Usage.Known {
			withUsage++
		}
	}))
	//: the warm-up call takes the first holder, the window the rest.
	holders := startHolders(t, r, collectedRuns+1)
	next := 0
	got := mallocsOver(collectedRuns, func() {
		h := &holders[next]
		next++
		_ = syscall.Close(h.release)
		h.release = -1
		deadline := time.Now().Add(collectDeadline)
		for {
			n, err := r.ReapOnce()
			if err != nil {
				sweepFailed = true
				return
			}
			if n > 0 {
				h.collected = true
				return
			}
			if !time.Now().Before(deadline) {
				timedOut = true
				return
			}
			runtime.Gosched()
		}
	})
	reportWindow(t, "collecting a child per call with WithOnExit", got)
	if sweepFailed || timedOut {
		t.Fatalf("a holder was not collected (sweep failed %v, timed out %v)", sweepFailed, timedOut)
	}
	//: every holder collected, each reported once, unowned, with its own status.
	if seen != collectedRuns+1 || unowned != seen || exited != seen {
		t.Fatalf("the observer saw %d exits, %d unowned, %d with status %d; want %d of each",
			seen, unowned, exited, reaper.HoldExitCode, collectedRuns+1)
	}
	//: the counters ride along exactly where the kernel fills them.
	if want := runtime.GOOS != "illumos" && runtime.GOOS != "solaris"; (withUsage == seen) != want {
		t.Errorf("%d of %d exits carried Usage, want all exactly where the kernel reports it (%v)", withUsage, seen, want)
	}
	//: zero per collected child, Usage in the event.
	if got.own != 0 {
		t.Errorf("%d collected children cost %d allocations, want 0", collectedRuns, got.own)
	}
}
