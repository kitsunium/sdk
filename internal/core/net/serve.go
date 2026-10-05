package net

import (
	"context"
)

// ServeConn implements ConnHandler by calling f.
func (f ConnHandlerFunc) ServeConn(ctx context.Context, c Conn) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, c)
}

// ServePacket implements PacketHandler by calling f.
func (f PacketHandlerFunc) ServePacket(ctx context.Context, p Packet) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, p)
}
