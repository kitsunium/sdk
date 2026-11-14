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
// by 30 % between the two reads on CI. The second case reproduces that cause
// on purpose: a goroutine touches 8 MiB and hands it back to the kernel in a
// loop, so the resident set swings by more than 20 % between any two instants,
// and the snapshot is taken only once that goroutine has touched its first
// block. The comparison with a single later read failed there 23 times in
// 2000.
func TestStatsReadsTheKernelsMemory(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		churn bool
	}
	runCase := func(t *testing.T, c tc) {
		//: the runtime moved on purpose, from its first touched block on.
		if c.churn {
			stop := make(chan struct{})
			started := make(chan struct{})
			done := make(chan struct{})
			go churnResidentSet(stop, started, done)
			defer func() {
				close(stop)
				<-done
			}()
			<-started
		}
		stats := bracketedStats(t)
		//: the peak, the threads and the descriptors the kernel counts.
		if stats.PeakRSSBytes < stats.RSSBytes/2 || stats.Threads < 1 || stats.OpenFiles < 3 {
			t.Fatalf("Stats = peak %d, threads %d, open files %d", stats.PeakRSSBytes, stats.Threads, stats.OpenFiles)
		}
	}
	cases := []tc{
		{name: "the runtime as it runs"},
		{name: "the runtime moving the resident set", churn: true},
	}
	//: each case in turn, the churn never beside the quiet one.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
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
// the kernel, until stop closes. It closes started once its first block is
// touched, so a snapshot taken after it sees the churn, and done when it
// returns.
func churnResidentSet(stop <-chan struct{}, started, done chan<- struct{}) {
	defer close(done)
	const block = 8 << 20
	page := os.Getpagesize()
	first := true
	//: until stop closes.
	for {
		select {
		//: the test is done.
		case <-stop:
			return
		//: one more swing.
		default:
		}
		churnSink = make([]byte, block)
		//: every page of the block made resident.
		for offset := 0; offset < block; offset += page {
			churnSink[offset] = 1
		}
		//: the first block is resident: the churn has begun.
		if first {
			close(started)
			first = false
		}
		churnSink = nil
		debug.FreeOSMemory()
	}
}
