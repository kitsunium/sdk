package logger_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// benchEnabled keeps Enabled's answer alive.
var benchEnabled bool

// BenchmarkEnabled_Fixed and BenchmarkEnabled_LevelVar price the level gate
// (#1015): a Logger built with MinLevel reads a level.Fixed through the
// Leveler interface, one built with Level reads a LevelVar's atomic.
func BenchmarkEnabled_Fixed(b *testing.B) {
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: logger.NewMemorySink(), MinLevel: logger.LevelInfo})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		benchEnabled = lg.Enabled(ctx, logger.LevelDebug)
	}
}

// BenchmarkEnabled_LevelVar is BenchmarkEnabled_Fixed over a live floor.
func BenchmarkEnabled_LevelVar(b *testing.B) {
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: logger.NewMemorySink(), Level: logger.NewLevelVar(logger.LevelInfo)})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		benchEnabled = lg.Enabled(ctx, logger.LevelDebug)
	}
}
