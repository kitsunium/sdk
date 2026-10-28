//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring wait, plain and grouped (ADR 0199), against the
// running kernel.
package loop

import (
	"runtime"
	"testing"
	"time"
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

// TestURingWaitArgs pins when the loop groups its wait (ADR 0199): only once
// the last batch reaped batchWaitFrom completions, with the grouped wait on,
// and never while draining.
func TestURingWaitArgs(t *testing.T) {
	t.Parallel()
	tick, grouped := &kernelTimespec{nsec: int64(tickEvery)}, &kernelTimespec{nsec: int64(defaultBatchWait)}
	for _, c := range []struct {
		name     string
		wait     time.Duration
		reaped   uint64
		draining bool
		ts       *kernelTimespec
		want     uint32
	}{
		{"idle", defaultBatchWait, 0, false, tick, 1},
		{"under the threshold", defaultBatchWait, batchWaitFrom - 1, false, tick, 1},
		{"loaded", defaultBatchWait, batchWaitFrom, false, grouped, uint32(defaultBatchWaitMin)},
		{"off", 0, 512, false, tick, 1},
		{"draining", defaultBatchWait, 512, true, tick, 1},
	} {
		ul := &uLoop{lp: &loop{sz: sizing{batchWait: c.wait, batchWaitMin: defaultBatchWaitMin}, draining: c.draining}, reaped: c.reaped}
		ts, want := ul.waitArgs(tick, grouped)
		if ts != c.ts || want != c.want {
			t.Fatalf("%s: waitArgs = (%v, %d), want (%v, %d)", c.name, ts, want, c.ts, c.want)
		}
	}
}
