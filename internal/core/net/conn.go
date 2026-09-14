// Package net — the accepted stream connection port.
package net

import (
	"context"
	stdnet "net"
)

// Conn is one accepted stream connection.
//
// It embeds stdlib net.Conn, so a handler reads and writes it exactly as it
// would any socket and every io helper keeps working — io.Copy, bufio, encoding
// wrappers, crypto/tls. Everything the domain adds is additive, which is what
// keeps "plug a handler in" a four-line exercise.
//
// A Conn is pooled and recycled once ServeConn returns. A handler MUST NOT
// retain it, nor the slice returned by Buffer, past that point — the same
// contract the logger's builder already carries.
//
// IFACE-PLUGIN: the concrete type stays unexported in internal/service/net/server.
type Conn interface {
	stdnet.Conn
	// ID returns the monotonic identifier assigned at accept time, for
	// correlating log lines belonging to one connection.
	ID() uint64
	// Group returns the name of the listener group that accepted this
	// connection, so one handler serving several groups can tell them apart.
	Group() string
	// Buffer returns a connection-scoped scratch buffer sized by the group's
	// read-buffer limit. It is recycled with the connection, which is what lets
	// a handler read without allocating per connection.
	Buffer() []byte
}

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
