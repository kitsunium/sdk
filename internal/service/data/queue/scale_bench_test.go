package queue_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// scaleEnv names the depths BenchmarkReceiveBacklog_Scale runs at,
// comma-separated (10000,100000,1000000 in .github/workflows/bench-ab.yml's
// scale mode, the suite queue-scale of scripts/ci/bench-ab.conf).
const scaleEnv string = "KITSUNIUM_BENCH_SCALE"

// BenchmarkReceiveBacklog_Scale is ReceiveBacklog's file row at the depths
// KITSUNIUM_BENCH_SCALE names — a Receive then a Nack over a backlog that
// moves at every iteration (#811) — the backlog written by FillReadyForTest
// without a flush, once a depth: its sub-benchmark runs twice (b.N = 1, then
// the -benchtime), and both read the same directory, which a lease handed back
// leaves as it was. Without the variable it skips: a million files is a run of
// its own, the bench A/B lane's scale mode, never `go test -bench=.`.
func BenchmarkReceiveBacklog_Scale(b *testing.B) {
	spec := os.Getenv(scaleEnv)
	if spec == "" {
		b.Skip(scaleEnv + " names no depth: the scale rows run in the bench A/B lane's scale mode")
	}
	parent := b.TempDir()
	for _, field := range strings.Split(spec, ",") {
		depth, err := strconv.Atoi(field)
		if err != nil || depth < 1 {
			b.Fatalf("%s=%q: a comma-separated list of positive depths", scaleEnv, spec)
		}
		var broker corequeue.Broker
		b.Run("file/depth"+field, func(b *testing.B) {
			if broker == nil {
				dir := filepath.Join(parent, field)
				fresh, newErr := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: benchPolicy()})
				if newErr != nil {
					b.Fatalf("NewFile() = %v", newErr)
				}
				if fillErr := svcqueue.FillReadyForTest(dir, depth, 64, time.Now().Add(-time.Hour)); fillErr != nil {
					b.Fatalf("FillReadyForTest(%d) = %v", depth, fillErr)
				}
				broker = fresh
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				leaseAndReturn(ctx, b, broker, 1)
			}
		})
	}
}
