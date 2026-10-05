package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Name returns the group's name.
func (g *PacketGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// Handle sets the group's handler, replacing any previous one.
func (g *PacketGroup) Handle(h corenet.PacketHandler) *PacketGroup {
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// HandleFunc sets the group's handler from a plain function.
func (g *PacketGroup) HandleFunc(f corenet.PacketHandlerFunc) *PacketGroup {
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
