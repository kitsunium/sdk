// Package server — a group of datagram listeners.
package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// PacketGroup is a set of datagram sockets sharing one handler, one middleware
// chain and one policy. It mirrors StreamGroup deliberately: the whole point of
// the domain is that a UDP service is wired the same way a TCP one is.
type PacketGroup struct {
	// name identifies the group in logs, metrics and State.
	name string
	// addrs are the addresses to bind.
	addrs []corenet.AddressValue
	// handler serves each received datagram.
	handler corenet.PacketHandler
	// middlewares decorate the handler, outermost first.
	middlewares []corenet.Middleware[corenet.PacketHandler]
	// limits bounds what the group may consume.
	limits corenet.LimitsValue
}

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
