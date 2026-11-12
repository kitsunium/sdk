package self_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// spin burns CPU on the calling goroutine until the kernel has measurably
// charged the process for it.
func spin(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(50 * time.Millisecond)
	counter := 0
	for time.Now().Before(deadline) {
		counter++
	}
	if counter == 0 {
		t.Fatal("the spin loop never ran")
	}
}

// TestReadStats pins that every figure is the process's own and consistent
// with the others: identity from the OS, the scheduler's settings, a heap
// that exists, a collection that has happened once the test forces one, and
// CPU time that grows when the process works.
func TestReadStats(t *testing.T) {
	t.Parallel()
	spin(t)
	//: after the spin, so the runtime's CPU estimate — taken at a collection
	//: where getrusage does not exist — has seen it too.
	runtime.GC()
	stats := self.ReadStats()
	if stats.PID != os.Getpid() || stats.MaxProcs != runtime.GOMAXPROCS(0) || stats.CPUs != runtime.NumCPU() {
		t.Errorf("identity = pid %d, maxprocs %d, cpus %d; want the process's own", stats.PID, stats.MaxProcs, stats.CPUs)
	}
	if stats.Goroutines < 1 || stats.HeapBytes == 0 || stats.HeapObjects == 0 || stats.TotalAllocBytes == 0 {
		t.Errorf("runtime figures = %+v, want a live heap and at least one goroutine", stats)
	}
	if stats.MemoryBytes < stats.HeapBytes {
		t.Errorf("MemoryBytes %d is below HeapBytes %d; the heap is part of what the runtime maps", stats.MemoryBytes, stats.HeapBytes)
	}
	if stats.GCCycles == 0 || stats.LastGC.IsZero() || stats.LastGC.After(stats.At) {
		t.Errorf("after runtime.GC: cycles %d, last %v, at %v", stats.GCCycles, stats.LastGC, stats.At)
	}
	if stats.GCPauses.Count() == 0 || stats.GCPauses.Quantile(0.99) <= 0 {
		t.Errorf("after runtime.GC the pause distribution holds %d, p99 %v", stats.GCPauses.Count(), stats.GCPauses.Quantile(0.99))
	}
	if stats.Started.After(stats.At) || stats.Uptime != stats.At.Sub(stats.Started) {
		t.Errorf("started %v, at %v, uptime %v: inconsistent", stats.Started, stats.At, stats.Uptime)
	}
	if stats.CPUTime <= 0 {
		t.Errorf("CPUTime = %v after 50 ms of spinning", stats.CPUTime)
	}
	//: the kernel's count wherever getrusage or GetProcessTimes exists, the
	//: estimate elsewhere — Windows is no longer one of the estimates (#938),
	//: unless the call fails, which for the current process it does not.
	wantEstimate := runtime.GOOS == "plan9" || runtime.GOOS == "js" || runtime.GOOS == "wasip1"
	if stats.CPUEstimated != wantEstimate {
		t.Errorf("CPUEstimated = %v on %s, want %v", stats.CPUEstimated, runtime.GOOS, wantEstimate)
	}
	later := self.ReadStats()
	if later.CPUTime < stats.CPUTime || later.TotalAllocBytes < stats.TotalAllocBytes || later.GCCycles < stats.GCCycles {
		t.Errorf("a cumulative figure went backwards between two snapshots")
	}
}

// TestReadStats_CPUTimeGrowsWithTheKernelsCount pins #938's Windows criterion
// on every platform that has the kernel's count: across a spin that the
// kernel itself — getrusage(2) or GetProcessTimes, read by the test between
// the two snapshots — charged at least 50 ms of CPU, CPUTime grows by no less
// than that charge, with no garbage collection asked for: the runtime's
// estimate, refreshed only at one, would not. The work is measured in CPU
// time, never in wall time, which a loaded machine gives without CPU.
func TestReadStats_CPUTimeGrowsWithTheKernelsCount(t *testing.T) {
	t.Parallel()
	if self.ReadStats().CPUEstimated {
		t.Skipf("%s has no kernel count of the process's CPU time, only the runtime's estimate", runtime.GOOS)
	}
	const (
		work   = 50 * time.Millisecond
		budget = 20 * time.Second
	)
	start := self.ReadStats()
	from, ok := processCPU()
	if !ok {
		t.Fatal("the kernel's count of the process's CPU time did not answer")
	}
	charged := spinCPU(t, from, work, budget)
	grew := self.ReadStats().CPUTime - start.CPUTime
	if charged < work {
		t.Skipf("the kernel charged %v of CPU in %v of spinning: the machine gave this process too little to judge", charged, budget)
	}
	if grew < charged {
		t.Errorf("CPUTime grew by %v while the kernel charged the process %v", grew, charged)
	}
}

// spinCPU burns CPU until the kernel has charged the process work since
// from, or budget of wall time has passed, and returns the charge.
func spinCPU(t *testing.T, from, work, budget time.Duration) time.Duration {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		now, ok := processCPU()
		if !ok {
			t.Fatal("the kernel's count of the process's CPU time stopped answering")
		}
		if now-from >= work {
			return now - from
		}
	}
	now, _ := processCPU()
	return now - from
}
