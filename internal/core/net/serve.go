package net

import (
	"context"
)

// serveConn is ConnHandlerFunc.ServeConn's body: decl_gen.go writes ConnHandlerFunc.ServeConn, from the
// design, as one call of it.
func (f ConnHandlerFunc) serveConn(ctx context.Context, c Conn) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, c)
}

// servePacket is PacketHandlerFunc.ServePacket's body: decl_gen.go writes PacketHandlerFunc.ServePacket, from the
// design, as one call of it.
func (f PacketHandlerFunc) servePacket(ctx context.Context, p Packet) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, p)
}
