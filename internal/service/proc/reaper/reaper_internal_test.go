//go:build unix

// Package reaper — white-box Unix tests: drain counting, Start/Stop lifecycle,
// concurrency safety, and the subreaper reparent-and-reap acceptance test.
package reaper

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// helperEnv gates the re-exec child role: when set, the test binary forks a
// detached, sleeping grandchild and exits, orphaning it onto the (subreaper)
// parent test process.
const helperEnv = "SDK_REAPER_ROLE_CHILD"

// grandchildEnv gates the re-exec grandchild role: when set, the test binary
// sleeps the requested seconds then exits, modelling the orphan to be reaped.
const grandchildEnv = "SDK_REAPER_ROLE_GRANDCHILD"

// TestMain intercepts the re-exec child/grandchild roles before the test runner
// starts, so a child can orphan a sleeping grandchild onto the parent test
// process. A bare `m.Run()` runs when neither role env is set.
func TestMain(m *testing.M) {
	//: the grandchild role just sleeps then exits — the orphan to be reaped.
	if s := os.Getenv(grandchildEnv); s != "" {
		//: sleep the requested seconds, then exit, bypassing the test suite.
		os.Exit(runGrandchild(s))
	}
	//: the child role forks the grandchild and exits, reparenting the orphan.
	if s := os.Getenv(helperEnv); s != "" {
		//: spawn-and-exit, bypassing the test suite.
		os.Exit(runChild(s))
	}
	//: normal path — run the package test suite.
	os.Exit(m.Run())
}

// runGrandchild sleeps for secs seconds (clamped to >=1) then returns 0, so the
// orphaned grandchild outlives its parent and is later collected by the reaper.
func runGrandchild(secs string) (code int) {
	//: parse the requested sleep; clamp a bad/zero value to one second.
	n, err := strconv.Atoi(secs)
	//: a malformed or non-positive value still terminates promptly.
	if err != nil || n <= 0 {
		//: clamp to one second.
		n = 1
	}
	//: outlive the parent so we become an orphan reparented to the subreaper.
	time.Sleep(time.Duration(n) * time.Second)
	//: clean exit; the subreaper will reap us.
	return 0
}

// runChild spawns a detached, sleeping grandchild then returns 0 so this child
// exits immediately, reparenting the grandchild onto the nearest subreaper.
func runChild(secs string) (code int) {
	//: launch the grandchild role of this same binary.
	cmd := exec.Command(os.Args[0])
	//: tag the spawned process as the grandchild and clear the child gate.
	cmd.Env = append(os.Environ(), helperEnv+"=", grandchildEnv+"="+secs)
	//: a fresh session fully detaches the grandchild from this child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	//: start without waiting — we exit next, orphaning the grandchild.
	if err := cmd.Start(); err != nil {
		//: non-zero status so the parent test observes the spawn failure.
		return 1
	}
	//: child exits now; the grandchild reparents to the subreaper.
	return 0
}

// TestDrainResultNoChildren asserts a sweep with no children returns (0, nil):
// ECHILD is a clean end, never an error.
func TestDrainResultNoChildren(t *testing.T) {
	//: not Parallel — Wait4(-1) reaps ANY child of this process, so reaper
	//: tests that spawn or count children must not run concurrently.
	r := &unixReaper{}
	//: with no children outstanding, the sweep must be a clean zero.
	got, err := r.drainResult()
	//: ECHILD must surface as nil, not ReapFailed.
	if err != nil {
		t.Fatalf("drainResult with no children returned error: %v", err)
	}
	//: nothing was reaped.
	if got != 0 {
		t.Fatalf("drainResult = %d, want 0", got)
	}
}

// TestDrainResultCountsChildren spawns N direct children that exit immediately
// and asserts a single drain sweep collects exactly N.
func TestDrainResultCountsChildren(t *testing.T) {
	//: serial — see TestDrainResultNoChildren rationale.
	const childCount int = 4
	//: spawn childCount trivial children that exit at once, becoming zombies.
	for i := range childCount {
		//: /bin/true (via the shell-free exec of "true") exits immediately.
		cmd := exec.Command("true")
		//: start without Wait so the child becomes a zombie for us to reap.
		if err := cmd.Start(); err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
	}
	//: give the children a moment to actually exit before we sweep.
	waitForZombies(t)
	r := &unixReaper{}
	//: one drain must collect every child to ECHILD.
	got, err := r.drainResult()
	//: a clean drain never errors.
	if err != nil {
		t.Fatalf("drainResult returned error: %v", err)
	}
	//: every spawned child must be accounted for in a single sweep.
	if got != childCount {
		t.Fatalf("drainResult = %d, want %d", got, childCount)
	}
}

// waitForZombies polls until at least one child has become reapable or a short
// deadline elapses, so the count assertions are not racing the children's exit.
func waitForZombies(t *testing.T) {
	t.Helper()
	//: poll a non-destructive WNOHANG|WNOWAIT-free probe is awkward; instead
	//: simply give the scheduler a brief, bounded grace period.
	deadline := time.Now().Add(2 * time.Second)
	//: spin with small sleeps until the deadline; children exit near-instantly.
	for time.Now().Before(deadline) {
		//: a short sleep is enough for `true` to exit on any sane host.
		time.Sleep(20 * time.Millisecond)
		//: one grace tick is sufficient; the count sweep does the real check.
		return
	}
}

// TestStartStopNoGoroutineLeak runs many Start/Stop cycles and asserts the
// goroutine count does not grow, proving the loop goroutine exits on Stop.
func TestStartStopNoGoroutineLeak(t *testing.T) {
	//: serial — the reaper installs a process-global SIGCHLD handler.
	r := New().(*unixReaper)
	//: a warm-up cycle settles any one-time runtime goroutines.
	r.Start()
	r.Stop()
	//: baseline after the loop goroutine has provably exited.
	base := runtime.NumGoroutine()
	//: repeated cycles must not accumulate goroutines.
	const cycles int = 20
	//: each cycle starts then fully stops (Stop blocks on loop exit).
	for range cycles {
		//: start then fully stop; Stop blocks until the loop goroutine exits.
		r.Start()
		r.Stop()
	}
	//: a small slack absorbs unrelated runtime goroutines; a leak would be ~20.
	if grew := runtime.NumGoroutine() - base; grew > 2 {
		t.Fatalf("goroutine count grew by %d across 20 Start/Stop cycles", grew)
	}
}

// TestConcurrentStopIsFullBarrier asserts that when several goroutines call Stop
// at once, every caller blocks until the loop goroutine has actually exited —
// not just the one that closes done. A concurrent Stop that returned early (on
// the old running=false-first path) could let a later Start race a still-running
// loop; here all callers must observe the loop gone before returning.
func TestConcurrentStopIsFullBarrier(t *testing.T) {
	//: serial — process-global SIGCHLD handler and goroutine accounting.
	r := New().(*unixReaper)
	//: bring the background loop up so there is a goroutine to join.
	r.Start()
	//: record the goroutine count while the loop is provably live.
	base := runtime.NumGoroutine()
	var wg sync.WaitGroup
	//: several goroutines race to Stop the same live loop.
	const stoppers int = 8
	//: each stopper must block until the loop has exited, never return early.
	for range stoppers {
		//: a concurrent Stop joins the same teardown barrier.
		wg.Go(func() {
			//: Stop must not return until the loop goroutine is gone.
			r.Stop()
		})
	}
	//: every concurrent Stop has returned.
	wg.Wait()
	//: after all Stops returned the loop goroutine must be gone, proving the
	//: barrier held for every caller and not only the closing one.
	if grew := runtime.NumGoroutine() - base; grew > 0 {
		t.Fatalf("loop goroutine survived concurrent Stop: count grew by %d", grew)
	}
	//: the reaper must be fully idle, so a fresh Start brings a clean loop up.
	r.mu.RLock()
	//: neither running nor stopping may linger once every Stop has returned.
	stillRunning, stillStopping := r.running, r.stopping
	r.mu.RUnlock()
	//: a lingering running/stopping flag would block a legitimate later Start.
	if stillRunning || stillStopping {
		t.Fatalf("after concurrent Stop: running=%v stopping=%v, want both false", stillRunning, stillStopping)
	}
	//: a later Start after a full-barrier Stop must launch a fresh loop cleanly.
	r.Start()
	//: tear the fresh loop down.
	r.Stop()
}

// TestStartIdempotentAndStopWithoutStart asserts Start is idempotent and Stop is
// safe without a prior Start.
func TestStartIdempotentAndStopWithoutStart(t *testing.T) {
	//: serial — process-global signal handler.
	r := New().(*unixReaper)
	//: Stop with no prior Start must be a harmless no-op.
	r.Stop()
	//: first Start brings the loop up.
	r.Start()
	//: a second Start while running must not panic or start a second loop.
	r.Start()
	//: only one running loop should exist; one Stop tears it down cleanly.
	r.Stop()
}

// TestReapOnceConcurrentWithLoop hammers ReapOnce from several goroutines while
// the background loop runs, asserting no data race and no panic. Run under -race
// this is the concurrency-safety acceptance check.
func TestReapOnceConcurrentWithLoop(t *testing.T) {
	//: serial at the package level; internal concurrency is the point.
	r := New().(*unixReaper)
	//: start the background loop so ReapOnce races a live sweeper.
	r.Start()
	//: ensure teardown regardless of assertion outcome.
	defer r.Stop()
	var wg sync.WaitGroup
	//: number of concurrent sweeping callers and sweeps per caller.
	const (
		callers    int = 8
		sweepsEach int = 50
	)
	//: several callers sweep concurrently with the loop and each other.
	for range callers {
		//: each goroutine performs a burst of concurrent ReapOnce calls.
		wg.Go(func() {
			//: a burst of sweeps exercises the shared-state guards.
			for range sweepsEach {
				//: a concurrent sweep must never error on an idle process.
				if _, err := r.ReapOnce(); err != nil {
					//: surface an unexpected reap failure to the test.
					t.Errorf("concurrent ReapOnce error: %v", err)
					return
				}
			}
		})
	}
	//: wait for every concurrent caller to finish.
	wg.Wait()
}

// TestSubreaperReapsOrphanedGrandchild is the issue-#63 acceptance test: with
// subreaper armed, a child forks a grandchild then exits, reparenting the
// grandchild onto this process; the reaper must collect it within one SIGCHLD
// cycle. When PR_SET_CHILD_SUBREAPER is unavailable, it skips but still asserts
// the typed-error contract was honoured.
func TestSubreaperReapsOrphanedGrandchild(t *testing.T) {
	//: serial — spawns processes and reaps ANY child of this test process.
	if err := SetChildSubreaper(); err != nil {
		//: subreaper unavailable (unprivileged/odd kernel) — assert the typed
		//: contract, then skip the behavioural half honestly.
		if !errs.HasCode(err, coreproc.CodeSubreaperFailed) {
			t.Fatalf("SetChildSubreaper failed with unexpected error: %v", err)
		}
		t.Skip("PR_SET_CHILD_SUBREAPER unavailable on this host; typed-error contract verified")
	}
	//: count reaped children via the observer so we can detect the grandchild.
	var (
		mu    sync.Mutex
		total int
	)
	//: a buffered ping channel signalling that at least one child was reaped.
	reaped := make(chan struct{}, 1)
	//: the observer accumulates the sweep counts and pings when any are reaped.
	onReap := func(n int) {
		//: ignore empty sweeps; only a real reap is interesting.
		if n == 0 {
			//: nothing collected this sweep.
			return
		}
		//: guard the shared tally against the loop goroutine.
		mu.Lock()
		total += n
		mu.Unlock()
		//: non-blocking signal that at least one child was collected.
		select {
		case reaped <- struct{}{}:
		default:
		}
	}
	r := New(WithOnReap(onReap)).(*unixReaper)
	//: start the SIGCHLD loop that will collect the orphaned grandchild.
	r.Start()
	//: ensure teardown.
	defer r.Stop()
	//: launch the helper child: it forks a 2s grandchild then exits, orphaning
	//: the grandchild onto this (subreaper) process.
	helper := exec.Command(os.Args[0])
	helper.Env = append(os.Environ(), helperEnv+"=2")
	//: start the helper; we will reap both it and (later) the grandchild.
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper child: %v", err)
	}
	//: poll up to 6s for the grandchild's exit to be reaped (2s sleep + slack).
	deadline := time.After(6 * time.Second)
	//: we expect at least two reaps overall: the helper child and the orphan.
	for {
		//: wait for either a reap ping or the deadline.
		select {
		case <-reaped:
			//: check whether the cumulative tally now covers child+grandchild.
			mu.Lock()
			got := total
			mu.Unlock()
			//: two collected children means the orphan was reparented and reaped.
			if got >= 2 {
				//: acceptance satisfied.
				return
			}
		case <-deadline:
			//: report the tally on timeout for diagnosis.
			mu.Lock()
			got := total
			mu.Unlock()
			t.Fatalf("subreaper did not reap orphaned grandchild in time; reaped=%d, want>=2", got)
		}
	}
}
