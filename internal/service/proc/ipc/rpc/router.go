package rpc

import (
	"context"

	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
)

// HandlerFunc is a function answering calls, as a Handler — what a Methods
// table holds most of the time.
type HandlerFunc func(ctx context.Context, req corerpc.Request) (reply any, err error)

// Methods is a Router over a fixed table: a method's handler is its entry.
// Build it before Serve and leave it alone: it is read without a lock.
type Methods map[string]corerpc.Handler

// ServeRPC calls f.
func (f HandlerFunc) ServeRPC(ctx context.Context, req corerpc.Request) (reply any, err error) {
	return f(ctx, req)
}

// Route returns the method's entry, and false when it has none.
func (m Methods) Route(method string) (h corerpc.Handler, ok bool) {
	h, ok = m[method]
	return h, ok
}
