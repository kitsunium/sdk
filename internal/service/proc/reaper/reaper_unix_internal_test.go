//go:build unix

// Package reaper — white-box Unix tests: drain counting, the Start/Stop
// lifecycle, and the concurrency guards.
//
// Wait4(-1) reaps ANY child of this process, and the loop installs a
// PROCESS-GLOBAL SIGCHLD handler. Two tests doing either at once would steal
// each other's children and each other's signals, so every test below takes
// reapMu for its whole body — as does the external test package, through the
// exported ReapLock. They are still marked parallel — the mutex is what makes
// that safe, and it keeps them from blocking the rest of the package.
package reaper

import (
	"os"
	"os/exec"
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
const helperEnv string = "SDK_REAPER_ROLE_CHILD"

// grandchildEnv gates the re-exec grandchild role: when set, the test binary
// sleeps the requested seconds then exits, modelling the orphan to be reaped.
const grandchildEnv string = "SDK_REAPER_ROLE_GRANDCHILD"

const (
	// reapDeadline bounds the wait for spawned children to exit and be reaped.
	// It is generous on purpose: the assertion is that every child IS reaped,
	// not that it happens quickly, so the deadline exists to fail instead of
	// hanging.
	reapDeadline time.Duration = 30 * time.Second
	// reapPoll is how long a sweep that found nothing waits before retrying.
	reapPoll time.Duration = 2 * time.Millisecond
)

// TestMain intercepts the re-exec child/grandchild roles before the test runner
// starts, so a child can orphan a sleeping grandchild onto the parent test
// process. A bare m.Run() runs when neither role env is set.
func TestMain(m *testing.M) {
	//: the grandchild role just sleeps then exits — the orphan to be reaped.
	if s := os.Getenv(grandchildEnv); s != "" {
		os.Exit(runGrandchild(s))
	}
	//: the child role forks the grandchild and exits, reparenting the orphan.
	if s := os.Getenv(helperEnv); s != "" {
		os.Exit(runChild(s))
	}
	//: normal path — run the package test suite.
	os.Exit(m.Run())
}

// runGrandchild sleeps for secs seconds (clamped to >=1) then returns 0, so the
// orphaned grandchild outlives its parent and is later collected by the reaper.
func runGrandchild(secs string) (code int) {
	n, err := strconv.Atoi(secs)
	//: a malformed or non-positive value still terminates promptly.
	if err != nil || n <= 0 {
		n = 1
	}
	//: outlive the parent so we become an orphan reparented to the subreaper.
	time.Sleep(time.Duration(n) * time.Second)
	return 0
}

// runChild spawns a detached, sleeping grandchild then returns 0 so this child
// exits immediately, reparenting the grandchild onto the nearest subreaper.
func runChild(secs string) (code int) {
	cmd := exec.Command(os.Args[0])
	//: tag the spawned process as the grandchild and clear the child gate.
	cmd.Env = append(os.Environ(), helperEnv+"=", grandchildEnv+"="+secs)
	//: a fresh session fully detaches the grandchild from this child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	//: start without waiting — we exit next, orphaning the grandchild.
	if err := cmd.Start(); err != nil {
		return 1
	}
	return 0
}

// Test_unixReaper_drainResult pins the sweep and its two terminal conditions.
//
// ECHILD ("this process has no children") is a CLEAN end, not a failure: an
// idle supervisor sweeps constantly and would otherwise log an error every time.
// A positive pid means keep going, and pid 0 with no error means "children
// exist, none have exited" — also a clean end.
//
// What one sweep returns is TIMING, not contract: it reaps the children that
// have already exited, and a child spawned a moment ago may not have. So the
// test sweeps until the total arrives, which is what the supervisor's own loop
// does. Asserting an exact count after a fixed sleep pins the scheduler
// instead — it passed under `go test` and failed under coverage
// instrumentation, where four children do not all exit inside 50ms.
func Test_unixReaper_drainResult(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many children to spawn before the sweep; each exits at once.
		children int
	}
	tests := []tc{
		{"no children at all", 0},
		{"one child", 1},
		{"four children", 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		for i := range c.children {
			//: `true` exits immediately, becoming a zombie for us to reap.
			cmd := exec.Command("true")
			if err := cmd.Start(); err != nil {
				t.Fatalf("spawning child %d: %v", i, err)
			}
		}

		r := &unixReaper{}
		total := 0
		deadline := time.Now().Add(reapDeadline)
		//: always sweep at least once, so the no-children case still pins that
		//: ECHILD comes back as a clean zero rather than as REAP_FAILED.
		for {
			got, err := r.drainResult()
			//: ECHILD must surface as nil, never as REAP_FAILED.
			if err != nil {
				t.Fatalf("drainResult = %v, want nil", err)
			}
			total += got
			//: every child accounted for, or out of patience.
			if total >= c.children || !time.Now().Before(deadline) {
				break
			}
			//: nothing had exited yet; let them.
			time.Sleep(reapPoll)
		}
		if total != c.children {
			t.Errorf("the sweeps reaped %d children, want %d", total, c.children)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_classifyWaitErr pins the three-way split every sweep depends
// on. EINTR is a retry, ECHILD is a clean end, and anything else is a real
// fault — collapsing any two of them either spins forever or reports an error
// on every idle sweep.
func Test_unixReaper_classifyWaitErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		err       error
		wantDone  bool
		wantFatal bool
	}
	tests := []tc{
		{"an interrupted call retries", syscall.EINTR, false, false},
		{"no children left is a clean end", syscall.ECHILD, true, false},
		{"a permission denial is fatal", syscall.EPERM, true, true},
		{"an invalid argument is fatal", syscall.EINVAL, true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var fired []int
		r := &unixReaper{onReap: func(n int) { fired = append(fired, n) }}

		done, fatal := r.classifyWaitErr(c.err, 3)

		if done != c.wantDone {
			t.Fatalf("classifyWaitErr(%v) done = %v, want %v", c.err, done, c.wantDone)
		}
		if (fatal != nil) != c.wantFatal {
			t.Fatalf("classifyWaitErr(%v) fatal = %v, want an error: %v", c.err, fatal, c.wantFatal)
		}
		if c.wantFatal {
			if !errs.HasCode(fatal, coreproc.CodeReapFailed) {
				t.Errorf("the fatal error is %v, want REAP_FAILED", fatal)
			}
		}
		//: the observer fires on every TERMINAL outcome so the sweep count is
		//: reported exactly once, whatever ended the sweep. A retry reports
		//: nothing, because the sweep has not finished.
		wantFires := 0
		if c.wantDone {
			wantFires = 1
		}
		if len(fired) != wantFires {
			t.Errorf("the observer fired %d times, want %d", len(fired), wantFires)
		}
		if wantFires == 1 && fired[0] != 3 {
			t.Errorf("the observer was told %d, want the running tally 3", fired[0])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_fireOnReap pins the nil tolerance. Most reapers have no
// observer, so the hook is nil far more often than not.
func Test_unixReaper_fireOnReap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		hooked bool
		counts []int
	}
	tests := []tc{
		{name: "no observer at all", counts: []int{0, 1, 5}},
		{name: "an observer sees every count", hooked: true, counts: []int{0, 1, 5}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var seen []int
		r := &unixReaper{}
		if c.hooked {
			r.onReap = func(n int) { seen = append(seen, n) }
		}

		for _, n := range c.counts {
			r.fireOnReap(n)
		}

		if !c.hooked {
			return
		}
		if len(seen) != len(c.counts) {
			t.Fatalf("the observer saw %v, want %v", seen, c.counts)
		}
		for i, want := range c.counts {
			if seen[i] != want {
				t.Errorf("call %d reported %d, want %d", i, seen[i], want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_drain pins the loop-side sweep: it records the outcome so a
// background failure stays visible, and it clears a previous error on a clean
// sweep so a transient fault does not stick forever.
func Test_unixReaper_drain(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: an error left over from an earlier sweep.
		stale error
	}
	tests := []tc{
		{name: "a clean sweep with no history"},
		{name: "a clean sweep clears a stale error", stale: syscall.EPERM},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		r := &unixReaper{lastErr: c.stale}

		r.drain()

		//: an idle process sweeps cleanly, so the recorded error must be nil —
		//: including when a previous sweep had failed.
		if got := r.LastError(); got != nil {
			t.Errorf("LastError() = %v after a clean sweep, want nil", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_LastError pins the snapshot read. The loop writes it from its
// own goroutine while a caller may read at any moment, so the lock is what keeps
// the value from being observed mid-write.
func Test_unixReaper_LastError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		readers int
	}
	tests := []tc{
		{"a single reader", 1},
		{"many concurrent readers", 32},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &unixReaper{}

		//: Goroutine lifecycle: one writer and c.readers readers, all joined by
		//: the WaitGroup before the assertion; none can outlive this case.
		var wg sync.WaitGroup
		wg.Go(func() {
			for range 100 {
				r.mu.Lock()
				r.lastErr = syscall.EPERM
				r.mu.Unlock()
			}
		})
		for range c.readers {
			wg.Go(func() {
				for range 100 {
					//: the value is irrelevant; the race detector is the
					//: check, so a non-nil reading is simply consumed.
					if err := r.LastError(); err != nil {
						continue
					}
				}
			})
		}
		wg.Wait()

		//: the last write stands.
		if r.LastError() == nil {
			t.Error("LastError() = nil after a recorded failure")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_ReapOnce pins the caller-facing sweep, which differs from the
// loop's in one way: it returns the error directly rather than recording it, so
// a caller sweeping on demand does not have to consult LastError.
func Test_unixReaper_ReapOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		hooks bool
	}
	tests := []tc{
		{name: "with no observer"},
		{name: "with an observer", hooks: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		var fired int
		r := &unixReaper{}
		if c.hooks {
			r.onReap = func(int) { fired++ }
		}

		got, err := r.ReapOnce()
		if err != nil {
			t.Fatalf("ReapOnce = %v, want nil", err)
		}
		if got < 0 {
			t.Errorf("ReapOnce = %d, want a non-negative count", got)
		}
		//: even an empty sweep is reported, which is what makes the hook usable
		//: as a liveness signal.
		if c.hooks && fired != 1 {
			t.Errorf("the observer fired %d times, want 1", fired)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_Start pins idempotency and the leak-free lifecycle.
//
// Start installs a process-global SIGCHLD handler, so a second Start while
// running must not install a second one — and every cycle's loop goroutine must
// be gone before the next begins, because a supervisor that reloads its config
// restarts the reaper every time.
//
// The proof is the per-cycle stopped channel rather than runtime.NumGoroutine:
// the loop closes it from its own final defer, so a closed channel IS an exited
// goroutine. A goroutine count cannot tell this package's goroutines from
// anyone else's, and is meaningless while other tests run.
func Test_unixReaper_Start(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		cycles int
		//: extra Start calls per cycle, which must all be no-ops.
		redundant int
	}
	tests := []tc{
		{name: "one cycle", cycles: 1},
		{name: "a redundant Start", cycles: 1, redundant: 3},
		{name: "twenty cycles", cycles: 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		r, ok := New().(*unixReaper)
		if !ok {
			t.Fatal("New did not return a unixReaper")
		}
		for cycle := range c.cycles {
			r.Start()
			//: capture this cycle's barrier before any Stop swaps it out.
			r.mu.RLock()
			stopped := r.stopped
			running := r.running
			r.mu.RUnlock()
			if !running {
				t.Fatalf("cycle %d: Start left the reaper idle", cycle)
			}
			for range c.redundant {
				//: a second Start while running must not raise a second loop,
				//: which would mean a second SIGCHLD handler.
				r.Start()
			}
			//: the redundant Starts must not have replaced the barrier — a new
			//: channel here would mean a new loop goroutine.
			r.mu.RLock()
			same := r.stopped == stopped
			r.mu.RUnlock()
			if !same {
				t.Fatalf("cycle %d: a redundant Start raised a second loop", cycle)
			}

			r.Stop()

			//: the loop closes stopped from its own final defer, so a closed
			//: channel is a goroutine that has provably returned.
			select {
			case <-stopped:
			default:
				t.Fatalf("cycle %d: the loop goroutine is still running after Stop", cycle)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_Stop pins the full barrier.
//
// Every concurrent Stop must block until the loop goroutine has ACTUALLY exited,
// not just the one that closed the done channel. A Stop that returned early
// would let a following Start race a still-running loop — two loops, two
// handlers, and a reaper that reaps twice.
func Test_unixReaper_Stop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		stoppers int
		//: whether a Stop is issued before any Start, which must be a no-op.
		stopFirst bool
	}
	tests := []tc{
		{name: "a Stop with no prior Start", stoppers: 0, stopFirst: true},
		{name: "a single Stop", stoppers: 1},
		{name: "eight concurrent Stops", stoppers: 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		r, ok := New().(*unixReaper)
		if !ok {
			t.Fatal("New did not return a unixReaper")
		}
		if c.stopFirst {
			//: a Stop with nothing running must be harmless.
			r.Stop()
			return
		}

		r.Start()
		//: capture the cycle's barrier while the loop is provably live.
		r.mu.RLock()
		stopped := r.stopped
		r.mu.RUnlock()

		//: Goroutine lifecycle: c.stoppers goroutines, each calling Stop once
		//: and returning; the WaitGroup joins them all before the assertions.
		var wg sync.WaitGroup
		for range c.stoppers {
			wg.Go(r.Stop)
		}
		wg.Wait()

		//: every Stop returned, so the loop must have closed its barrier — the
		//: proof that each caller waited for the goroutine rather than only the
		//: one that closed done.
		select {
		case <-stopped:
		default:
			t.Error("a concurrent Stop returned before the loop goroutine exited")
		}
		//: and the reaper must be fully idle, or a later Start would refuse.
		r.mu.RLock()
		stillRunning, stillStopping := r.running, r.stopping
		r.mu.RUnlock()
		if stillRunning || stillStopping {
			t.Errorf("after Stop: running=%v stopping=%v, want both false", stillRunning, stillStopping)
		}
		//: a fresh cycle must launch cleanly.
		r.Start()
		r.Stop()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_markStopped pins the ordering that makes the barrier correct.
// The loop clears the cycle flags BEFORE closing stopped, so the first Stop
// caller to wake observes an idle reaper rather than a torn, mid-teardown state.
func Test_unixReaper_markStopped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		running  bool
		stopping bool
	}
	tests := []tc{
		{"a running cycle", true, false},
		{"a cycle already tearing down", true, true},
		{"an idle reaper", false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &unixReaper{running: c.running, stopping: c.stopping}

		r.markStopped()

		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.running || r.stopping {
			t.Errorf("after markStopped: running=%v stopping=%v, want both false", r.running, r.stopping)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_loop pins the two things the loop owes its owner: a final
// drain after the stop request, so no zombie outlives Stop, and a detached
// SIGCHLD subscription, so a stopped reaper stops receiving signals.
func Test_unixReaper_loop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many children exist when the stop is requested; the final drain
		//: must collect them.
		children int
	}
	tests := []tc{
		{"no children at stop time", 0},
		{"children still to collect at stop time", 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		var mu sync.Mutex
		var total int
		r, ok := New(WithOnReap(func(n int) {
			mu.Lock()
			total += n
			mu.Unlock()
		})).(*unixReaper)
		if !ok {
			t.Fatal("New did not return a unixReaper")
		}

		r.Start()
		for i := range c.children {
			cmd := exec.Command("true")
			if err := cmd.Start(); err != nil {
				r.Stop()
				t.Fatalf("spawning child %d: %v", i, err)
			}
		}
		//: wait for the children to be accounted for rather than for a fixed
		//: duration: WHEN each one exits is the scheduler's business, and a
		//: sleep that is long enough on an idle machine is not long enough
		//: under coverage instrumentation.
		deadline := time.Now().Add(reapDeadline)
		for {
			mu.Lock()
			got := total
			mu.Unlock()
			//: every child collected, or out of patience.
			if got >= c.children || !time.Now().Before(deadline) {
				break
			}
			//: nothing new yet; give the loop another SIGCHLD to work with.
			time.Sleep(reapPoll)
		}
		r.Stop()

		mu.Lock()
		defer mu.Unlock()
		//: every child is accounted for by the time Stop returns, whether the
		//: SIGCHLD path or the final drain collected it — that is the promise
		//: Stop makes, and the count is how a caller sees it kept.
		if total != c.children {
			t.Errorf("the loop reaped %d children, want %d", total, c.children)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixReaper_ReapOnceConcurrent hammers ReapOnce from several goroutines
// while the background loop runs. Wait4 is kernel-serialised, so the count is
// simply split across whoever observes each exit — but the shared state around
// it is not, and this is where an unguarded field would show up under -race.
func Test_unixReaper_ReapOnceConcurrent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		callers    int
		sweepsEach int
	}
	tests := []tc{
		{"a few callers", 4, 25},
		{"many callers", 8, 50},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reapMu.Lock()
		defer reapMu.Unlock()

		r, ok := New().(*unixReaper)
		if !ok {
			t.Fatal("New did not return a unixReaper")
		}
		r.Start()
		defer r.Stop()

		//: Goroutine lifecycle: c.callers goroutines, each sweeping a fixed
		//: number of times and returning; the WaitGroup joins them all.
		var wg sync.WaitGroup
		for range c.callers {
			wg.Go(func() {
				for range c.sweepsEach {
					//: an idle process never fails a sweep.
					if _, err := r.ReapOnce(); err != nil {
						t.Errorf("a concurrent ReapOnce = %v, want nil", err)
						return
					}
				}
			})
		}
		wg.Wait()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
