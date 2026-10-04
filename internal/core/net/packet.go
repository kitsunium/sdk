// Package net — the received datagram port.
package net

import (
	"context"
)

// PacketHandlerFunc adapts a plain function to PacketHandler.
type PacketHandlerFunc func(ctx context.Context, p Packet) error

// ServePacket implements PacketHandler by calling f.
func (f PacketHandlerFunc) ServePacket(ctx context.Context, p Packet) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, p)
}
