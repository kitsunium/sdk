package net

// The func adapters must satisfy the ports they exist to adapt; an adapter that
// drifts from its interface is useless and the drift is silent until call time.
var (
	_ ConnHandler   = ConnHandlerFunc(nil)
	_ PacketHandler = PacketHandlerFunc(nil)
	_ HTTP1Handler  = HTTP1HandlerFunc(nil)
)
