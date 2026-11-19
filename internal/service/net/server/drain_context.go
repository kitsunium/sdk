package server

import (
	"context"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// DrainContext returns a context derived from ctx that is also cancelled when
// the server serving this request begins draining, and its cancel function,
// which the caller must call. It is DrainSignal for a wait that takes a context
// — a long poll on a queue, a blocking receive — so the wait ends as the drain
// starts instead of holding the drain open until its budget expires. On a
// context that carries no drain signal it is context.WithCancel(ctx).
//
// Goroutine lifecycle: one per call on a context that carries a drain signal,
// ended when the drain starts or the returned context ends, whichever is first.
func DrainContext(ctx context.Context) (context.Context, context.CancelFunc) {
	drain := corenet.DrainSignal(ctx)
	derived, cancel := context.WithCancel(ctx)
	//: nothing announces a drain: the parent alone ends it.
	if drain == nil {
		//: a plain child.
		return derived, cancel
	}
	go func() {
		select {
		case <-drain:
			cancel()
		case <-derived.Done():
		}
	}()
	//: the watcher ends with the context, whichever ends it.
	return derived, cancel
}
