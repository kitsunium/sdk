//go:build unix && !race

package reaper_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// TestReapOnceAllocatesNothingWithOrWithoutOnExit is #1136's allocation row:
// an idle sweep allocates nothing, and wiring WithOnExit keeps it so — the
// observer is bound once at New and the status it is shown is the ledger's
// own buffer, never a heap copy. Race off: the race runtime's shadow
// allocations would count; the alloc lane runs it (tools/alloc-lane-targets.txt).
func TestReapOnceAllocatesNothingWithOrWithoutOnExit(t *testing.T) {
	for name, r := range map[string]interface{ ReapOnce() (int, error) }{
		"without an observer": reaper.New(),
		"with WithOnExit":     reaper.New(reaper.WithOnExit(func(reaper.ExitEvent) {})),
	} {
		//: a host that cannot sweep says nothing about allocations.
		if _, err := r.ReapOnce(); err != nil {
			t.Skipf("ReapOnce is not usable here: %v", err)
		}
		//: zero, not merely few.
		if allocs := testing.AllocsPerRun(100, func() { _, _ = r.ReapOnce() }); allocs != 0 {
			t.Errorf("ReapOnce %s allocates %v per sweep, want 0", name, allocs)
		}
	}
}
