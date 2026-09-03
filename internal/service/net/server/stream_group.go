// Package server — a group of listeners sharing one handler and policy.
package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// StreamGroup is a set of listeners sharing one handler, one middleware chain
// and one policy. Grouping exists so a server can expose the same handler on a
// TCP port and a Unix socket — or two ports with different TLS identities —
// without the caller assembling the wiring twice.
type StreamGroup struct {
	// name identifies the group in logs, metrics and State.
	name string
	// addrs are the addresses to bind.
	addrs []corenet.AddressValue
	// handler serves each accepted connection.
	handler corenet.ConnHandler
	// middlewares decorate the handler, outermost first.
	middlewares []corenet.Middleware[corenet.ConnHandler]
	// identity turns the group's listeners into TLS or mTLS listeners.
	identity corenet.IdentityValue
	// limits bounds what the group may consume.
	limits corenet.LimitsValue
	// timeouts bounds each phase of a connection.
	timeouts corenet.TimeoutsValue
}

// Name returns the group's name.
func (g *StreamGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// Handle sets the group's handler, replacing any previous one.
func (g *StreamGroup) Handle(h corenet.ConnHandler) *StreamGroup {
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// HandleFunc sets the group's handler from a plain function. It exists because
// the overwhelmingly common case is one function, and making that case require
// a named type would be the papercut that decides whether the API feels light.
func (g *StreamGroup) HandleFunc(f corenet.ConnHandlerFunc) *StreamGroup {
	//: the func adapter already satisfies the port.
	return g.Handle(f)
}

// Use appends middlewares, outermost first.
func (g *StreamGroup) Use(middlewares ...corenet.Middleware[corenet.ConnHandler]) *StreamGroup {
	g.middlewares = append(g.middlewares, middlewares...)
	//: returned for chaining.
	return g
}

// resolved returns the handler with the group's middlewares applied.
func (g *StreamGroup) resolved() corenet.ConnHandler {
	//: composed once at Start rather than per connection, so the middleware
	//: chain costs nothing on the hot path.
	return corenet.Chain(g.handler, g.middlewares...)
}
