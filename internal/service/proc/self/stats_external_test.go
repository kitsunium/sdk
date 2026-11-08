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
// on every platform that has the kernel's count: CPUTime rises by at least
// 40 ms across 50 ms of spinning, with no garbage collection between the two
// snapshots — the runtime's estimate, refreshed only at one, would not.
func TestReadStats_CPUTimeGrowsWithTheKernelsCount(t *testing.T) {
	t.Parallel()
	before := self.ReadStats()
	if before.CPUEstimated {
		t.Skipf("%s has no kernel count of the process's CPU time, only the runtime's estimate", runtime.GOOS)
	}
	//: a machine under load can deschedule the spinning thread for part of
	//: its 50 ms, so the criterion is tried a few times: a count that does not
	//: follow the work fails every one of them.
	const (
		attempts = 5
		floor    = 40 * time.Millisecond
	)
	var grew time.Duration
	for range attempts {
		start := self.ReadStats()
		spin(t)
		grew = self.ReadStats().CPUTime - start.CPUTime
		if grew >= floor {
			return
		}
	}
	t.Errorf("CPUTime grew by %v across 50 ms of spinning, want at least %v", grew, floor)
}
