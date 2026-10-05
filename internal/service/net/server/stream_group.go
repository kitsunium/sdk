package server

import (
	"net/http"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Name returns the group's name.
func (g *StreamGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// handle is StreamGroup.Handle's body: decl_gen.go writes StreamGroup.Handle, from the
// design, as one call of it.
func (g *StreamGroup) handle(h corenet.ConnHandler) *StreamGroup {
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// handleFunc is StreamGroup.HandleFunc's body: decl_gen.go writes StreamGroup.HandleFunc, from the
// design, as one call of it.
func (g *StreamGroup) handleFunc(f corenet.ConnHandlerFunc) *StreamGroup {
	//: the func adapter already satisfies the port.
	return g.Handle(f)
}

// handleHTTP is StreamGroup.HandleHTTP's body: decl_gen.go writes StreamGroup.HandleHTTP, from the
// design, as one call of it.
func (g *StreamGroup) handleHTTP(h http.Handler) *StreamGroup {
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

// use is StreamGroup.Use's body: decl_gen.go writes StreamGroup.Use, from the
// design, as one call of it.
func (g *StreamGroup) use(middlewares ...corenet.Middleware[corenet.ConnHandler]) *StreamGroup {
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
		g.httpAdapter.bounds = g.http
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
