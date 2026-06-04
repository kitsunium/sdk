package nettransport

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// BenchmarkWrite measures the per-record Write path with a no-op send seam, so
// the reported cost is the SDK overhead alone (one mutex round-trip + the send
// call) without any real socket. The conn seam ships the caller's payload
// synchronously and verbatim, so this path allocates nothing — the 0-alloc
// invariant the file sink also holds. (HTTP egress, by contrast, allocates per
// request and is intentionally not on this path.)
func BenchmarkWrite(b *testing.B) {
	s := newNetSink("tcp", func(context.Context, []byte) error { return nil }, nil)
	payload := []byte("benchmark log line\n")
	ctx := b.Context()
	b.ReportAllocs()
	//: b.Loop() manages timer reset and iteration count (Go 1.24+).
	for b.Loop() {
		//: a Write failure aborts the measurement.
		if _, werr := s.Write(ctx, corelogger.RecordEvent{}, payload); werr != nil {
			b.Fatalf("Write: %v", werr)
		}
	}
}
