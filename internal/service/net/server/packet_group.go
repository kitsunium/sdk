package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Name returns the group's name.
func (g *PacketGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// handle is PacketGroup.Handle's body: decl_gen.go writes PacketGroup.Handle, from the
// design, as one call of it.
func (g *PacketGroup) handle(h corenet.PacketHandler) *PacketGroup {
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// handleFunc is PacketGroup.HandleFunc's body: decl_gen.go writes PacketGroup.HandleFunc, from the
// design, as one call of it.
func (g *PacketGroup) handleFunc(f corenet.PacketHandlerFunc) *PacketGroup {
	//: the func adapter already satisfies the port.
	return g.Handle(f)
}

// Use appends middlewares, outermost first.
func (g *PacketGroup) Use(middlewares ...corenet.Middleware[corenet.PacketHandler]) *PacketGroup {
	g.middlewares = append(g.middlewares, middlewares...)
	//: returned for chaining.
	return g
}

// resolved returns the handler with the group's middlewares applied.
func (g *PacketGroup) resolved() corenet.PacketHandler {
	//: composed once at Start rather than per datagram, so the chain costs
	//: nothing on the hot path.
	return corenet.Chain(g.handler, g.middlewares...)
}
