//go:build unix

// Package reaper_test — the reaper beside the process API it shares a parent
// with.
//
// A running reaper answers every SIGCHLD with wait4(-1), and wait4(-1) collects
// ANY child of the process — including one a Process handle is waiting for.
// Whichever of the two reached the kernel first took the exit status. When the
// sweep won, the handle's own wait failed with ECHILD, and a child that had
// exited 0 came back as WAIT_FAILED: a supervisor running as pid 1, the one
// place the reaper is switched on, read that as a failure and restarted a
// service that had stopped cleanly.
//
// These tests switch the reaper on, spawn children that exit 0, and require
// every Wait to report exactly that. Each holds the binary-wide reap lock for
// its whole body, because the loop and what it collects are process-global.
package reaper_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
	svcreaper "github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// handoffRunsEnv overrides how many children each case spawns. The default
// keeps the suite quick; a measurement run raises it to put a figure on a rate.
const handoffRunsEnv string = "SDK_REAPER_HANDOFF_RUNS"

const (
	// defaultHandoffRuns is how many children each case spawns by default.
	defaultHandoffRuns int = 200
	// shortHandoffRuns is the default under -test.short: the e2e-cross lane
	// runs this suite in QEMU guests where every fork/exec is emulated.
	shortHandoffRuns int = 50
	// handoffDeadline bounds the wait for the reaper to collect a child in the
	// late case, so a reaper that never sweeps fails the test instead of
	// hanging it.
	handoffDeadline time.Duration = 30 * time.Second
	// handoffPoll paces the late case's check for a collected child.
	handoffPoll time.Duration = time.Millisecond
	// handoffShell is the program every child runs. POSIX requires it at this
	// path, so its absence is a broken host rather than a reason to pass.
	handoffShell string = "/bin/sh"
)

// TestWaitGetsTheExitWhileTheReaperRuns spawns children that exit 0 while the
// reaper loop runs, and requires every Wait to return code 0 with no error.
//
// Two orders are covered, because the reaper can take a status at two moments:
//
//   - at once: Wait is called as soon as Start returns, which is what a
//     supervisor does. The handle and the sweep race for the same zombie, and
//     how often the sweep wins depends on the scheduler — the rate is what this
//     case measures.
//   - late: Wait is held back until the child can no longer be signalled, i.e.
//     until something has already reaped it, and nothing but the reaper could
//     have. The handle is then left with no zombie of its own to wait for, so
//     the only way it can learn the exit status is from whoever took it. This
//     case does not depend on the scheduler at all.
func TestWaitGetsTheExitWhileTheReaperRuns(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// late holds Wait back until a sweep has collected the child.
		late bool
		// workers is how many goroutines spawn and wait concurrently. Several at
		// once also makes SIGCHLDs arrive together and merge into one sweep.
		workers int
	}
	tests := []tc{
		{"Wait called at once, one spawner", false, 1},
		{"Wait called at once, eight concurrent spawners", false, 8},
		{"Wait called after the reaper collected the child", true, 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		requireShell(t)
		//: the loop is process-global and so is what it collects.
		svcreaper.ReapLock()
		defer svcreaper.ReapUnlock()

		r := svcreaper.New()
		r.Start()
		defer r.Stop()

		runs := handoffRuns(t)
		failures := spawnAndWait(t, runs, c.workers, c.late)
		//: every single Wait must have seen the exit; one loss is the bug.
		if len(failures) > 0 {
			t.Errorf("%d of %d children that exited 0 were not reported as such; first: %s",
				len(failures), runs, failures[0])
		}
	}
	//: run every case as its own subtest; the reap lock serialises them.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// spawnAndWait runs `runs` children across `workers` goroutines and returns a
// description of every Wait that did not report a clean exit 0.
func spawnAndWait(t *testing.T, runs, workers int, late bool) []string {
	t.Helper()
	var (
		mu       sync.Mutex
		failures []string
		wg       sync.WaitGroup
	)
	jobs := make(chan int)
	record := func(msg string) {
		mu.Lock()
		failures = append(failures, msg)
		mu.Unlock()
	}
	//: start the spawners, each draining the shared job queue.
	for range workers {
		wg.Go(func() {
			//: each worker drains the shared job queue.
			for i := range jobs {
				//: a spawn failure is the host's problem, not the hand-off's.
				if msg := spawnOne(t.Context(), i, late); msg != "" {
					record(msg)
				}
			}
		})
	}
	//: hand out one job per child, then close the queue.
	for i := range runs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return failures
}

// spawnOne starts one child that exits 0, optionally waits until the reaper has
// collected it, then Waits. It returns "" when Wait reported exactly exit 0, and
// a description of what it reported otherwise.
func spawnOne(ctx context.Context, i int, late bool) string {
	p, err := svcexec.Start(ctx, coreproc.Spec{
		Path: handoffShell,
		Args: []string{"sh", "-c", "exit 0"},
	})
	//: a spawn failure is reported, but as itself, not as a lost status.
	if err != nil {
		return fmt.Sprintf("run %d: Start: %v", i, err)
	}
	//: the late order waits for a sweep to take the zombie before Wait runs.
	if late {
		//: a reaper that never collects the child fails the run, not the suite.
		if gone := awaitCollected(p.PID()); gone != nil {
			return fmt.Sprintf("run %d (pid %d): %v", i, p.PID(), gone)
		}
	}
	ev, err := p.Wait()
	//: a Wait error is exactly the defect: the status existed and was lost.
	if err != nil {
		return fmt.Sprintf("run %d (pid %d): Wait error %v (code %d)", i, p.PID(), err, ev.Code)
	}
	//: a clean exit must come back as code 0, not signalled.
	if ev.Code != 0 || ev.Signaled {
		return fmt.Sprintf("run %d (pid %d): Wait = code %d signaled=%t, want code 0", i, p.PID(), ev.Code, ev.Signaled)
	}
	return ""
}

// awaitCollected blocks until pid can no longer be signalled — a zombie still
// can be, so ESRCH means the zombie itself is gone, reaped by the only other
// waiter in the process: the reaper. It gives up after handoffDeadline.
func awaitCollected(pid int) error {
	deadline := time.Now().Add(handoffDeadline)
	//: poll until the zombie is gone, or out of patience.
	for time.Now().Before(deadline) {
		//: signal 0 checks existence without delivering anything.
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(handoffPoll)
	}
	return errors.New("the reaper never collected the child")
}

// handoffRuns returns how many children each case spawns: the default (a
// smaller one under -test.short), or the positive integer in handoffRunsEnv.
func handoffRuns(t *testing.T) int {
	t.Helper()
	raw := os.Getenv(handoffRunsEnv)
	//: no override — the quick default.
	if raw == "" {
		//: an emulated guest pays for every fork; keep the short run short.
		if testing.Short() {
			return shortHandoffRuns
		}
		return defaultHandoffRuns
	}
	n, err := strconv.Atoi(raw)
	//: a malformed override is a mistake in the measurement, not a pass.
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q: want a positive integer", handoffRunsEnv, raw)
	}
	return n
}

// requireShell skips the test where no executable /bin/sh exists — every child
// runs it, and a host without one cannot say anything about the hand-off. The
// exec package gates its spawn tests the same way.
func requireShell(t *testing.T) {
	t.Helper()
	info, err := os.Stat(handoffShell)
	//: a missing or non-executable shell is the host's gap, not the reaper's.
	if err != nil || info.Mode()&0o111 == 0 {
		t.Skipf("%s is not an executable file here (%v): no child can be spawned", handoffShell, err)
	}
}
