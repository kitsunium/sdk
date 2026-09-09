// Package server — the completion state of one connection handed to net/http.
package server

import (
	"sync"
	"sync/atomic"
)

// connWaiter is what a ServeConn blocks on while net/http drives a connection.
//
// It carries the hijack flag alongside the completion channel because the two
// answer one question together: net/http reports a finished connection and a
// hijacked one through the same terminal hook, and the engine must treat them
// oppositely — close the first, let go of the second.
type connWaiter struct {
	// done is closed once net/http is finished with the connection, whether it
	// closed it or a handler took it over.
	done chan struct{}
	// hijacked records that a handler took the socket over. It is atomic
	// because ServeConn can wake on the drain instead of on done, and would
	// then read this while net/http's goroutine is still writing it.
	hijacked atomic.Bool
	// doneOnce guards close(done). The waiter is deleted from the map under the
	// adapter's lock before it is released, which already makes the close
	// single; the guard is explicit so the invariant is local to the close
	// rather than inferred from a delete three lines away.
	doneOnce sync.Once
}

// release wakes the ServeConn waiting on this connection, exactly once.
func (w *connWaiter) release() {
	//: several terminal states can reach one connection; only one may close.
	w.doneOnce.Do(func() {
		//: the engine goroutine holding this connection is free to return.
		close(w.done)
	})
}
