//go:build !race

package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probe.
var allocSink any

// TestV116BuildSendAllocatesOnePerEmit pins the measured reality the BENCH.md
// prose must reflect: the chainable Build().Send() hot path is NOT
// allocation-free. The handler clones the accumulated attrs scratchpad
// (mergeAttrs / slices.Clone) on every Send, so exactly one heap slice escapes
// per emit even though the sync.Pool recycles the *chainBuilder itself.
// Regression for V116 — before the fix BENCH.md claimed the byte cost
// "drops to near-zero" on this path. Carries //go:build !race
// (testing.AllocsPerRun reports +1 under -race) and no t.Parallel
// (AllocsPerRun reads a process-global counter, so concurrent allocations
// from sibling subtests would corrupt the measurement). Run via
// `bazel test --config=pure //pkg/v1/logger:logger_test`.
func TestV116BuildSendAllocatesOnePerEmit(t *testing.T) {
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    discardSink{},
		Encoder: logger.TextEncoder(),
	})
	if err != nil {
		t.Fatalf("NewWithSink err = %v", err)
	}
	ctx := t.Context()
	type tc struct {
		name string
		want float64
	}
	tests := []tc{
		{name: "Build().Send() costs one heap alloc per emit", want: 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: warm the pool so the measured runs hit the recycled-builder path.
		logger.Build(lg, logger.LevelInfo).Str("warm", "up").Send(ctx, "warm")
		//: drive the chainable path; the handler's attrs clone escapes per Send.
		got := testing.AllocsPerRun(2000, func() {
			b := logger.Build(lg, logger.LevelInfo).
				Str("k1", "v1").
				Str("k2", "v2").
				Int("n1", 1)
			allocSink = b
			b.Send(ctx, "msg")
		})
		//: the path costs at least one alloc/op — never the documented "near-zero".
		if got < tc.want {
			t.Errorf("%s: Build().Send() allocs/op = %v, want >= %v (the handler's attrs clone)", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
