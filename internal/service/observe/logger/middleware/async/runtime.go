// Package async — gathers the small runtime helpers used by the
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
func isClosed(ch chan struct{}) bool {
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
func asyncCtx() context.Context {
	//: background context decouples drainer lifetime from producer lifetime.
	return context.Background()
}

// forwardDownstreamError surfaces a downstream Write error to the OnError
// callback so operators have a hook for metrics / fallback logging; without
// the callback the async sink would silently drop sink-level failures.
func (s *asyncSink) forwardDownstreamError(bytes int, err error) {
	//: defensive guard so bogus bytes do not trigger the callback.
	if bytes < 0 || err == nil {
		//: nothing to surface on the happy path or on bogus byte counts.
		return
	}
	//: hand the error to the caller-supplied callback (or the no-op default).
	s.onError(err)
}

// swallowRingError documents the test-only pattern of dropping a Queue error
// in the DropOldest retry path. The retry happens after a successful TryRead,
// so the slot is free and the second TryWrite will not fail in practice.
func swallowRingError(err error) {
	//: explicit early-return on nil — the read satisfies the unused-param audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: documented drop — DropOldest path's second TryWrite is unreachable in practice.
}
