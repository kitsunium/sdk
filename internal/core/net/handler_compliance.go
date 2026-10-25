package net

// The func adapters must satisfy the ports they exist to adapt; an adapter that
// drifts from its interface is useless and the drift is silent until call time.
// LoopLayer forwards every callback a layer may be asked for: it must stay a
// LoopHandler, and each optional capability, or a layer embedding it would
// stop forwarding one without a compile error.
var (
	_ ConnHandler        = ConnHandlerFunc(nil)
	_ PacketHandler      = PacketHandlerFunc(nil)
	_ HTTP1Handler       = HTTP1HandlerFunc(nil)
	_ LoopHandler        = LoopLayer{}
	_ LoopLayered        = LoopLayer{}
	_ LoopOptionHonourer = LoopLayer{}
	_ LoopStarter        = LoopLayer{}
)
