//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"fmt"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkURingBatchBudget measures a held burst of budgetConns lines of
// budgetWork each, with no budget and with 1, 2 and 5 ms: the delay from the
// release to the first, median and last echo (first-ms, p50-ms, last-ms).
// Under no burst a budget costs nothing more than a clock read a reap:
// BenchmarkURingRings_fill measures that load.
func BenchmarkURingBatchBudget(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*budgetConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, budget := range []time.Duration{0, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond} {
		b.Run(fmt.Sprintf("budget=%v", budget), func(b *testing.B) {
			_, hold, held, release, holder, conns := budgetGroup(b, budget, 1)
			var firstSum, midSum, lastSum time.Duration
			n := 0
			for b.Loop() {
				lat := burst(b, hold, held, release, holder, conns)
				firstSum += lat[0]
				midSum += lat[len(lat)/2]
				lastSum += lat[len(lat)-1]
				n++
			}
			ms := func(d time.Duration) float64 { return float64(d) / float64(n) / float64(time.Millisecond) }
			b.ReportMetric(ms(firstSum), "first-ms")
			b.ReportMetric(ms(midSum), "p50-ms")
			b.ReportMetric(ms(lastSum), "last-ms")
		})
	}
}
