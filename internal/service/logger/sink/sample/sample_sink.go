// Package sample implements a 1-of-N rate-limiter Sink. Every Nth Write
// is forwarded to the downstream sink; the rest are silently dropped. The
// counter is atomic so concurrent producers stay correct without a mutex.
//
// Use case: emit verbose Debug records at a 1/100 rate to a remote drain
// while keeping the local console stream intact (compose with sink/multi
// from commit 11 for the per-branch policy).
package sample

import (
	"context"
	"sync/atomic"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// sampleSink keeps every Nth Write; the rest are discarded. The counter
// is atomic so concurrent producers do not need a mutex on the hot path.
type sampleSink struct {
	// downstream receives every Nth Write.
	downstream corelogger.Sink
	// rate is the sampling denominator — every Nth call is forwarded.
	rate uint64
	// counter tracks the producer call count; atomically incremented per Write.
	counter atomic.Uint64
}

// New constructs a sample Sink that forwards every Nth Write to the
// downstream sink.
//
// Params:
//   - downstream: the wrapped sink that receives the kept records.
//   - rate: keep 1 of every N writes; rate <= 0 is rejected.
//
// Returns:
//   - corelogger.Sink: the sample Sink behind the public interface.
//   - error: RateInvalid when rate <= 0; DownstreamNil when downstream is nil.
func New(downstream corelogger.Sink, rate int) (sink corelogger.Sink, err error) {
	//: refuse a non-positive rate — the modulo would panic.
	if rate <= 0 {
		//: documented sentinel — caller must supply a positive rate.
		return nil, RateInvalid
	}
	//: refuse a nil downstream — kept records would have nowhere to land.
	if downstream == nil {
		//: documented sentinel — caller must supply a downstream sink.
		return nil, DownstreamNil
	}
	//: hand back the sink behind the public Sink interface.
	return &sampleSink{downstream: downstream, rate: uint64(rate)}, nil
}

// Write forwards every Nth call to the downstream sink. Dropped calls
// return (0, nil) so callers cannot tell the difference.
//
// Params:
//   - ctx: request-scoped context forwarded to the downstream sink.
//   - rec: originating record forwarded to the downstream sink.
//   - p: formatted bytes forwarded to the downstream sink.
//
// Returns:
//   - n: bytes accepted by the downstream sink (0 on dropped writes).
//   - err: downstream's error on kept writes; nil on dropped writes.
func (s *sampleSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: atomic increment so concurrent producers stay correct without a mutex.
	count := s.counter.Add(1)
	//: keep this write only when the counter aligns with the sampling rate.
	if count%s.rate == 0 {
		//: forward the kept record to the downstream sink.
		return s.downstream.Write(ctx, rec, p)
	}
	//: documented contract — dropped writes return (0, nil) silently.
	return 0, nil
}

// Flush forwards to the downstream sink so its own buffers settle.
//
// Params:
//   - ctx: request-scoped context forwarded to the downstream sink.
//
// Returns:
//   - err: downstream's Flush error; nil on success.
func (s *sampleSink) Flush(ctx context.Context) (err error) {
	//: delegate to the downstream sink — the sample wrapper has no buffers.
	return s.downstream.Flush(ctx)
}

// Close forwards to the downstream sink so its own resources release.
//
// Returns:
//   - err: downstream's Close error; nil on success.
func (s *sampleSink) Close() (err error) {
	//: delegate to the downstream sink — the sample wrapper has no resources.
	return s.downstream.Close()
}
