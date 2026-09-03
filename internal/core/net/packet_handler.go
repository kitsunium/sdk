// Package net — the datagram handler port.
package net

import "context"

// PacketHandler serves one received datagram.
//
// Returning an error does not kill the server: it is logged and metered, and
// reading continues. A datagram service must survive a malformed packet.
//
// Implementations MUST be safe for concurrent use.
type PacketHandler interface {
	ServePacket(ctx context.Context, p Packet) error
}

// PacketHandlerFunc adapts a plain function to PacketHandler.
type PacketHandlerFunc func(ctx context.Context, p Packet) error

// ServePacket implements PacketHandler by calling f.
func (f PacketHandlerFunc) ServePacket(ctx context.Context, p Packet) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, p)
}
