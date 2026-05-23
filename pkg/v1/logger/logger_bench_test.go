// Benchmarks for the logger package — two scenarios matching the
// convention adopted by zap/zerolog: a static string and a record
// carrying a structured set of attrs (the size of benchAttrs10).
// Comparison baseline is Go std slog (the most natural reference —
// also structured, also stdlib).
//
// Methodology disclosed in pkg/v1/logger/BENCH.md alongside the
// numbers. The zero-alloc claim in /workspace/CLAUDE.md is auditable
// here: if the `BenchmarkLogger10Fields` row shows `0 allocs/op`,
// the claim holds; otherwise the claim must be retracted.
//
// Run: `cd pkg/v1/logger && go test -bench=. -benchmem -run='^$'`
package logger_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// discardSink implements logger.Sink (= corelogger.Sink) by dropping
// every record. Using a custom sink lets the bench focus on the
// serialisation + dispatch path without the I/O of a real transport.
// Flush + Close are no-ops — required by the Sink interface contract.
type discardSink struct{}

func (discardSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}

func (discardSink) Flush(_ context.Context) error { return nil }
func (discardSink) Close() error                  { return nil }

// Attribute set reused across both libraries' structured-record
// benches so the comparison is apples-to-apples.
var benchAttrs10 = []logger.Attr{
	logger.String("k1", "v1"),
	logger.String("k2", "v2"),
	logger.String("k3", "v3"),
	logger.String("k4", "v4"),
	logger.String("k5", "v5"),
	logger.Int("n1", 1),
	logger.Int("n2", 2),
	logger.Int("n3", 3),
	logger.Int("n4", 4),
	logger.Int("n5", 5),
}

func newKitsuniumLogger(b *testing.B) logger.Logger {
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    discardSink{},
		Encoder: logger.TextEncoder(),
	})
	if err != nil {
		b.Fatalf("logger init: %v", err)
	}
	return lg
}

func newSlogLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// BenchmarkLoggerStaticString — cheapest path. No fields, just the
// message. Mirrors zap's "static string" scenario.
func BenchmarkLoggerStaticString(b *testing.B) {
	ctx := b.Context()
	lg := newKitsuniumLogger(b)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		logger.Info(ctx, lg, "static")
	}
}

func BenchmarkSlogStaticString(b *testing.B) {
	lg := newSlogLogger()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		lg.Info("static")
	}
}

// BenchmarkLogger10Fields — record + 10 attrs every call. This is the
// zero-alloc claim's load-bearing scenario.
func BenchmarkLogger10Fields(b *testing.B) {
	ctx := b.Context()
	lg := newKitsuniumLogger(b)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		logger.Info(ctx, lg, "event", benchAttrs10...)
	}
}

func BenchmarkSlog10Fields(b *testing.B) {
	lg := newSlogLogger()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		lg.Info("event",
			"k1", "v1", "k2", "v2", "k3", "v3", "k4", "v4", "k5", "v5",
			"n1", 1, "n2", 2, "n3", 3, "n4", 4, "n5", 5)
	}
}
