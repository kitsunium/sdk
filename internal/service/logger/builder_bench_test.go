package logger_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// BenchmarkBuild_ZeroAlloc validates the zero-allocation claim of the
// chainable Builder API once the recycler is warm. Run with -benchmem to
// confirm allocs/op == 0 in steady state.
func BenchmarkBuild_ZeroAlloc(b *testing.B) {
	var buf bytes.Buffer
	lg := mustBuilderLogger(b, &buf, level.Debug)
	//: warm the pool so the steady-state path is exercised.
	svclogger.Build(lg, level.Info).Str("k", "v").Send(b.Context(), "warm")
	buf.Reset()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		svclogger.Build(lg, level.Info).
			Str("k", "v").
			Int("n", 7).
			Send(b.Context(), "msg")
	}
}
