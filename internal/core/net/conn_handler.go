// Package net — the stream handler port.
package net

import "context"

// ConnHandler serves one accepted stream connection.
//
// Returning an error does not kill the server: the connection is closed and the
// error is logged and metered. That is deliberate — one malformed peer must
// never be able to take down the process.
//
// Implementations MUST be safe for concurrent use: the server runs one
// goroutine per connection and they all share the handler.
type ConnHandler interface {
	ServeConn(ctx context.Context, c Conn) error
}

// ConnHandlerFunc adapts a plain function to ConnHandler.
type ConnHandlerFunc func(ctx context.Context, c Conn) error

// ServeConn implements ConnHandler by calling f.
func (f ConnHandlerFunc) ServeConn(ctx context.Context, c Conn) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, c)
}
