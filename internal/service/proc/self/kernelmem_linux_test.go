package self_test

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// bracketAttempts bounds the snapshots a bracketed comparison takes: each
// costs three reads of procfs, tens of microseconds, and one whose two
// bracketing reads saw no movement of the resident set decides.
const bracketAttempts = 1000

// TestStatsReadsTheKernelsMemory pins #939 on Linux: RSSBytes is the resident
// pages /proc/self/statm counts, times the page size; the peak is at least
// half the current set; threads and descriptors are counted; KernelMemory says
// the figure is the kernel's.
//
// The resident set is read by the test just before and just after the
// snapshot, and RSSBytes must lie between those two reads (#1356): comparing
// it with one read taken at another instant judged the runtime instead — a
// parallel test's goroutines, a new span, a collection's madvise moved the set
// by 30 % between the two reads on CI.
func TestStatsReadsTheKernelsMemory(t *testing.T) {
	t.Parallel()
	stats := bracketedStats(t)
	if stats.PeakRSSBytes < stats.RSSBytes/2 || stats.Threads < 1 || stats.OpenFiles < 3 {
		t.Fatalf("Stats = peak %d, threads %d, open files %d", stats.PeakRSSBytes, stats.Threads, stats.OpenFiles)
	}
}

// TestStatsReadsTheKernelsMemoryWhileTheRuntimeMovesIt reproduces #1356's
// cause on purpose: a goroutine touches 8 MiB and hands it back to the kernel
// in a loop, so the resident set swings by more than 20 % between any two
// instants, and RSSBytes still lies between the two reads that bracket it.
// The comparison with a single later read failed here 23 times in 2000.
func TestStatsReadsTheKernelsMemoryWhileTheRuntimeMovesIt(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go churnResidentSet(stop, done)
	defer func() {
		close(stop)
		<-done
	}()
	bracketedStats(t)
}

// bracketedStats takes snapshots until one's RSSBytes lies between the
// resident set read just before it and just after it, and returns that one.
// A snapshot that is not the kernel's fails at once; one that never falls
// between its brackets in bracketAttempts tries is not the kernel's count.
func bracketedStats(t *testing.T) self.StatsValue {
	t.Helper()
	var (
		stats         self.StatsValue
		before, after uint64
	)
	for range bracketAttempts {
		before = residentBytes(t)
		stats = self.ReadStats()
		after = residentBytes(t)
		if !stats.KernelMemory {
			t.Fatalf("KernelMemory = false, RSSBytes = %d: the snapshot is not the kernel's", stats.RSSBytes)
		}
		if min(before, after) <= stats.RSSBytes && stats.RSSBytes <= max(before, after) {
			return stats
		}
	}
	t.Fatalf("RSSBytes = %d, never between the kernel's reads around it in %d tries (last %d and %d)",
		stats.RSSBytes, bracketAttempts, before, after)
	return stats
}

// residentBytes is /proc/self/statm's resident pages times the page size,
// read by the test itself.
func residentBytes(t *testing.T) uint64 {
	t.Helper()
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skipf("no /proc/self/statm: %v", err)
	}
	fields := strings.Fields(string(statm))
	if len(fields) < 2 {
		t.Fatalf("/proc/self/statm = %q: no resident field", statm)
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("/proc/self/statm resident field %q: %v", fields[1], err)
	}
	return pages * uint64(os.Getpagesize())
}

// churnSink keeps the churned block reachable until it is dropped, so the
// compiler cannot elide the allocation.
var churnSink []byte

// churnResidentSet touches 8 MiB a page at a time, drops it and returns it to
// the kernel, until stop closes; it closes done when it returns.
func churnResidentSet(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	const block = 8 << 20
	page := os.Getpagesize()
	for {
		select {
		case <-stop:
			return
		default:
		}
		churnSink = make([]byte, block)
		for offset := 0; offset < block; offset += page {
			churnSink[offset] = 1
		}
		churnSink = nil
		debug.FreeOSMemory()
	}
}
