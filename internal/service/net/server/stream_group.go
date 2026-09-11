// Package server — a group of listeners sharing one handler and policy.
package server

import (
	"net/http"

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
	// httpAdapter is set when the group serves an http.Handler, so the engine
	// can shut the embedded http.Server down with the rest of the group.
	httpAdapter *httpAdapter
	// limiter caps concurrent connections; nil when the group has no ceiling.
	limiter *connLimiter
	// adopt names inherited sockets to take over instead of binding.
	adopt []string
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

// HandleHTTP serves an http.Handler over this group's listeners.
//
// The group keeps its own limits, TLS identity and drain; net/http only does
// the protocol. That split is ADR 0029 D3: reimplementing HTTP would mean
// owning request smuggling defences, HTTP/2 flow control and HPACK to lose,
// not gain, throughput.
func (g *StreamGroup) HandleHTTP(h http.Handler) *StreamGroup {
	adapter := newHTTPAdapter(h)
	g.httpAdapter = adapter
	//: the adapter IS a ConnHandler, so the rest of the engine is unchanged.
	return g.Handle(adapter)
}

// tracksCloses reports whether the group's sockets must report their own
// Close: a handler can take one over — the group serves HTTP — while a ceiling
// still has to count it. It is read at bind time, after the limiter is built.
func (g *StreamGroup) tracksCloses() bool {
	//: every other group either cannot hijack or has no slot to hold.
	return g.httpAdapter != nil && g.limiter != nil
}

// Use appends middlewares, outermost first.
func (g *StreamGroup) Use(middlewares ...corenet.Middleware[corenet.ConnHandler]) *StreamGroup {
	g.middlewares = append(g.middlewares, middlewares...)
	//: returned for chaining.
	return g
}

// resolved returns the handler with the group's middlewares applied.
func (g *StreamGroup) resolved() corenet.ConnHandler {
	//: the adapter learns the group's deadlines here, at Start, which is the
	//: first moment every option is certainly applied and still before any
	//: accept loop exists to read them.
	if g.httpAdapter != nil {
		g.httpAdapter.timeouts = g.timeouts
	}
	//: composed once at Start rather than per connection, so the middleware
	//: chain costs nothing on the hot path.
	chained := corenet.Chain(g.handler, g.middlewares...)
	//: a group with no TLS identity has no negotiation to bound, so it is not
	//: wrapped at all and pays nothing.
	if g.identity.IsZero() {
		//: plaintext: straight to the handler chain.
		return chained
	}
	//: wrapped once per group, so bounding the handshake costs no allocation
	//: per connection.
	return handshakeHandler{next: chained, timeouts: g.timeouts}
}
