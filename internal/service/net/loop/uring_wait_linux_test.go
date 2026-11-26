//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring wait, plain and grouped (ADR 0199), against the
// running kernel.
package loop

import (
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// waitSlack is how late a wait may return past its bound on a loaded CI
// runner, and how long "at once" may take.
const waitSlack = 200 * time.Millisecond

// openWaitRing opens a ring of 16 with flags on the locked test thread, or
// skips where the kernel has no io_uring for this process
// (TestBackendsRequired is what fails a lane that requires it).
func openWaitRing(t *testing.T, flags uint32) *uring {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	maskPreemption(t)
	u, step, errno := openURing(16, flags)
	if errno != 0 {
		t.Skipf("io_uring %s: %v", step, errno)
	}
	t.Cleanup(u.close)
	return u
}

// nops queues n NOPs, published but not submitted: the wait submits them.
func nops(t *testing.T, u *uring, n int) {
	t.Helper()
	for range n {
		s := u.get()
		if s == nil {
			t.Fatal("no SQE")
		}
		s.opcode = opNop
	}
}

// TestURingWait pins what io_uring_enter does with the loop's two waits, on
// a COOP_TASKRUN ring and on a DEFER_TASKRUN one: the plain wait returns on
// one completion; the grouped wait on its count, or at its bound with what
// came — an ETIME with completions in the CQ is no error, and one with none
// is no retry —; and a completion already in the CQ makes either a submit.
func TestURingWait(t *testing.T) {
	for _, ring := range []struct {
		name  string
		flags uint32
	}{
		{"coop", setupBase},
		{"defer", setupBase&^setupCoopTaskrun | setupSingleIssuer | setupDeferTaskrun},
	} {
		t.Run(ring.name, func(t *testing.T) {
			for _, c := range []struct {
				name           string
				queued, nops   int
				ts             time.Duration
				want           uint32
				ready          uint32
				least, longest time.Duration
			}{
				{"plain: one completion", 0, 1, time.Second, 1, 1, 0, waitSlack},
				{"grouped: the count", 0, 8, time.Second, 8, 8, 0, waitSlack},
				{"grouped: the bound with one", 0, 1, 20 * time.Millisecond, 8, 1, 20 * time.Millisecond, 20*time.Millisecond + waitSlack},
				{"grouped: the bound with none", 0, 0, 20 * time.Millisecond, 8, 0, 20 * time.Millisecond, 20*time.Millisecond + waitSlack},
				{"grouped: one already there", 1, 0, time.Second, 8, 0, 0, waitSlack},
			} {
				t.Run(c.name, func(t *testing.T) {
					u := openWaitRing(t, ring.flags)
					tick := kernelTimespec{nsec: int64(time.Second)}
					//: a completion left in the CQ, unreaped.
					if c.queued > 0 {
						nops(t, u, c.queued)
						u.wait(&tick, uint32(c.queued))
					}
					nops(t, u, c.nops)
					ts := kernelTimespec{nsec: int64(c.ts)}
					start := time.Now()
					u.wait(&ts, c.want)
					took := time.Since(start)
					if got := u.ready(); got != c.ready+uint32(c.queued) {
						t.Fatalf("ready = %d, want %d", got, c.ready+uint32(c.queued))
					}
					if took < c.least || took > c.longest {
						t.Fatalf("returned after %v, want within [%v, %v]", took, c.least, c.longest)
					}
					if u.retries != 0 || u.pending() != 0 {
						t.Fatalf("retries %d, pending %d: the wait's ETIME was taken for a busy kernel", u.retries, u.pending())
					}
				})
			}
		})
	}
}

// TestURingWaitArgs pins when the loop groups its wait (ADR 0199, ADR 0202):
// once the last batch reaped batchWaitFrom completions, or reaped some while
// two arrivals at least come within the bound; with the grouped wait on, and
// never while draining.
func TestURingWaitArgs(t *testing.T) {
	t.Parallel()
	tick, grouped := &kernelTimespec{nsec: int64(tickEvery)}, &kernelTimespec{nsec: int64(defaultBatchWait)}
	for _, c := range []struct {
		name     string
		wait     time.Duration
		reaped   uint64
		spacing  time.Duration
		draining bool
		ts       *kernelTimespec
		want     uint32
	}{
		{"idle", defaultBatchWait, 0, 0, false, tick, 1},
		{"under the threshold, sparse", defaultBatchWait, batchWaitFrom - 1, tickEvery, false, tick, 1},
		{"under the threshold, one an arrival a bound", defaultBatchWait, 1, defaultBatchWait, false, tick, 1},
		{"under the threshold, two an arrival a bound", defaultBatchWait, 1, defaultBatchWait / 2, false, grouped, uint32(defaultBatchWaitMin)},
		{"frequent but the last batch empty", defaultBatchWait, 0, time.Microsecond, false, tick, 1},
		{"loaded", defaultBatchWait, batchWaitFrom, tickEvery, false, grouped, uint32(defaultBatchWaitMin)},
		{"off", 0, 512, 0, false, tick, 1},
		{"draining", defaultBatchWait, 512, 0, true, tick, 1},
	} {
		ul := &uLoop{lp: &loop{sz: sizing{batchWait: c.wait, batchWaitMin: defaultBatchWaitMin}, draining: c.draining}, reaped: c.reaped, spacing: c.spacing}
		ts, want := ul.waitArgs(tick, grouped)
		if ts != c.ts || want != c.want {
			t.Fatalf("%s: waitArgs = (%v, %d), want (%v, %d)", c.name, ts, want, c.ts, c.want)
		}
	}
}

// TestURingWoke pins the moving average of the time between two arrivals: a
// batch with none leaves it, a steady rate settles it near its spacing however
// the arrivals are grouped into batches.
func TestURingWoke(t *testing.T) {
	t.Parallel()
	ul := &uLoop{lp: &loop{}, spacing: tickEvery}
	ul.lp.started = time.Second
	ul.reaped = 3
	ul.woke()
	if ul.spacing != tickEvery || ul.lastArrival != 0 {
		t.Fatalf("a batch with no arrival moved the average: %v, %v", ul.spacing, ul.lastArrival)
	}
	for i := range 128 {
		ul.arrived = uint64(1 + i%3)
		ul.lp.started += time.Duration(ul.arrived) * 100 * time.Microsecond
		ul.woke()
	}
	if ul.spacing < 100*time.Microsecond || ul.spacing > 110*time.Microsecond {
		t.Fatalf("after 128 batches of arrivals 100 µs apart the average is %v", ul.spacing)
	}
}

// sigURG is the signal the Go runtime sends a thread to preempt the goroutine
// on it.
const sigURG = 23

// maskPreemption blocks the runtime's preemption signal on the calling,
// locked thread until the test ends. A signal delivered inside
// io_uring_enter ends it with EINTR whatever its timeout (io_cqring_wait
// returns -EINTR on task_sigpending, io_uring/io_uring.c), and the runtime
// sends one to any goroutine it finds running for 10 ms, which a loaded runner
// makes of the test between two statements: the waits below, which pin the
// kernel's bound, would return after 60 to 100 µs (#1490). The loop itself
// takes an EINTR for what it is — a wake with nothing to reap — and loops.
func maskPreemption(t *testing.T) {
	t.Helper()
	set, old := uint64(1)<<(sigURG-1), uint64(0)
	const sigBlock, sigSetmask = 0, 2
	if _, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, sigBlock, uintptr(unsafe.Pointer(&set)), uintptr(unsafe.Pointer(&old)), 8, 0, 0); e != 0 {
		t.Fatalf("rt_sigprocmask: %v", e)
	}
	t.Cleanup(func() {
		//: the thread goes back to the runtime with the mask it had.
		syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, sigSetmask, uintptr(unsafe.Pointer(&old)), 0, 8, 0, 0)
	})
}

// TestURingWaitInterrupted pins that a signal landing in the wait cannot
// shorten what TestURingWait measures: the preemption signal is sent to the
// waiting thread 1 ms into a 20 ms bound, as the runtime does to a thread it
// finds busy.
func TestURingWaitInterrupted(t *testing.T) {
	u := openWaitRing(t, setupBase&^setupCoopTaskrun|setupSingleIssuer|setupDeferTaskrun)
	pid, tid := syscall.Getpid(), syscall.Gettid()
	done := make(chan struct{})
	defer func() { <-done }()
	go func() {
		defer close(done)
		time.Sleep(time.Millisecond)
		syscall.Tgkill(pid, tid, syscall.Signal(sigURG))
	}()
	ts := kernelTimespec{nsec: int64(20 * time.Millisecond)}
	start := time.Now()
	u.wait(&ts, 8)
	if took := time.Since(start); took < 20*time.Millisecond {
		t.Fatalf("returned after %v, want the 20ms bound: a signal ended io_uring_enter with EINTR", took)
	}
}
