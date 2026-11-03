//go:build !race

// Package statsbridge_test — the allocation gate behind #871's claim: an
// adapter's Observe, the bridge's per-engine work at every collection,
// allocates nothing. Race-off lane only (the race detector allocates on its
// own); //internal/service/observe/metrics/statsbridge:statsbridge_test is
// listed in tools/alloc-lane-targets.txt.
package statsbridge_test

import (
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// TestObserveIsAllocationFree: every adapter reports its statistics, named
// and attributed, without an allocation.
func TestObserveIsAllocationFree(t *testing.T) {
	var sink int64
	report := coremetrics.ReportStat(func(_ string, _ coremetrics.StatKind, _ string, value int64, attrs ...coreotel.AttrValue) {
		sink += value + int64(len(attrs))
	})
	for i, observer := range engines(t) {
		observer.Observe(report)
		if allocs := testing.AllocsPerRun(200, func() { observer.Observe(report) }); allocs != 0 {
			t.Errorf("adapter %d: Observe allocated %.1f times per call, want 0", i, allocs)
		}
	}
	_ = sink
}
