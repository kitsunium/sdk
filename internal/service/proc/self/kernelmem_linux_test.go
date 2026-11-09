package self_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// TestStatsReadsTheKernelsMemory pins #939 on Linux: RSSBytes is within 20 %
// of /proc/self/statm's resident pages times the page size, read by the test
// itself; the peak is at least the current set; threads and descriptors are
// counted; KernelMemory says the figure is the kernel's.
func TestStatsReadsTheKernelsMemory(t *testing.T) {
	t.Parallel()
	stats := self.ReadStats()
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skipf("no /proc/self/statm: %v", err)
	}
	pages, _ := strconv.ParseUint(strings.Fields(string(statm))[1], 10, 64)
	want := float64(pages * uint64(os.Getpagesize()))
	got := float64(stats.RSSBytes)
	if !stats.KernelMemory || got < 0.8*want || got > 1.2*want {
		t.Fatalf("RSSBytes = %d (kernel %v), want within 20%% of %d", stats.RSSBytes, stats.KernelMemory, uint64(want))
	}
	if stats.PeakRSSBytes < stats.RSSBytes/2 || stats.Threads < 1 || stats.OpenFiles < 3 {
		t.Fatalf("Stats = peak %d, threads %d, open files %d", stats.PeakRSSBytes, stats.Threads, stats.OpenFiles)
	}
}
