package rotfile

import (
	"path/filepath"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// BenchmarkWrite measures the Write path with the interval daemon running, so
// the reported cost includes any contention the ticker introduces (none in
// steady state — the tick fires hourly). Writes stay well under MaxBytes so no
// rotation happens in the measured loop. allocs are amortised, not a hard zero
// (this is a deferring on-disk sink, not the producer hot path).
func BenchmarkWrite(b *testing.B) {
	dir := b.TempDir()
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	sink, oerr := newRotatingSink(&Config{
		Path: filepath.Join(dir, "bench.log"), MaxBytes: 1 << 30,
		RotateEvery: time.Hour, Clock: &fakeClock{now: now},
	})
	//: construction failure aborts the benchmark.
	if oerr != nil {
		b.Fatalf("newRotatingSink: %v", oerr)
	}
	rs := sink.(*rotatingSink)
	//: join the daemon on exit so the benchmark leaks no goroutine.
	defer func() {
		//: surface a close failure rather than discarding it.
		if cerr := rs.Close(); cerr != nil {
			b.Errorf("close: %v", cerr)
		}
	}()
	payload := []byte("benchmark log line\n")
	ctx := b.Context()
	b.ReportAllocs()
	//: b.Loop() manages timer reset and iteration count (Go 1.24+).
	for b.Loop() {
		//: a Write failure aborts the measurement.
		if _, werr := rs.Write(ctx, corelogger.RecordEvent{}, payload); werr != nil {
			b.Fatalf("Write: %v", werr)
		}
	}
}
