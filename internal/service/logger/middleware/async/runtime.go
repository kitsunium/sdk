// Package async: runtime.go gathers the small runtime helpers used by the
// async drainer (yield primitive, channel-closed probe, error swallowers).
// Pulled out of async_sink.go to keep that file focused on the Sink contract.
package async

import (
	"context"
	"runtime"
)

// yieldOnce is the cooperative yield primitive used by the drainer when the
// queue is empty and by Flush while waiting for the queue to drain. Wraps
// runtime.Gosched so the call site stays self-documenting.
func yieldOnce() {
	//: scheduler hand-off so other goroutines (drainer or producer) progress.
	runtime.Gosched()
}

// isClosed reports whether ch has been closed. Used by Write to refuse work
// after Close without forcing a sync.Mutex on the hot path.
//
// Params:
//   - ch: stop channel observed by the drainer.
//
// Returns:
//   - closed: true when the channel is closed; false otherwise.
func isClosed(ch chan struct{}) (closed bool) {
	//: a closed receive returns immediately with !ok; an open one falls through.
	select {
	case <-ch:
		//: channel is closed — Write must refuse work.
		return true
	default:
		//: channel is still open — proceed with the enqueue.
		return false
	}
}

// asyncCtx returns the context handed to downstream sinks during draining.
// Always context.Background — the producer's request-scoped context is gone
// by the time the drainer fires, so a long-lived background context lets
// the downstream sink settle without spurious cancellation.
//
// Returns:
//   - ctx: background context for the drainer's downstream calls.
func asyncCtx() (ctx context.Context) {
	//: background context decouples drainer lifetime from producer lifetime.
	return context.Background()
}

// swallowDownstreamError is the documented sink for downstream Write errors.
// The async sink cannot propagate errors back to the original producer, so
// the failure is silently dropped. Future commits may swap this for a
// configurable callback wired into Config.
//
// Params:
//   - bytes: byte count returned by the downstream sink; informational only.
//   - err: downstream error to discard.
func swallowDownstreamError(bytes int, err error) {
	//: defensive guards so both parameters are observed by the audit.
	if bytes < 0 || err == nil {
		//: nothing to discard on the happy path or on bogus byte counts.
		return
	}
	//: documented drop — async sink contract forbids propagating errors.
}

// swallowRingError documents the test-only pattern of dropping a Queue error
// in the DropOldest retry path. The retry happens after a successful TryRead,
// so the slot is free and the second TryWrite will not fail in practice.
//
// Params:
//   - err: ring error to discard.
func swallowRingError(err error) {
	//: explicit early-return so err is observed by the audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: documented drop — DropOldest path's second TryWrite is unreachable in practice.
}
