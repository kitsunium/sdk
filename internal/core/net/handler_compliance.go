// Package net — compile-time interface assertions, kept out of the production
// source per KTN-IFACE-ASSERT-PLACEMENT.
package net

// The func adapters must satisfy the ports they exist to adapt; an adapter that
// drifts from its interface is useless and the drift is silent until call time.
var (
	_ ConnHandler   = ConnHandlerFunc(nil)
	_ PacketHandler = PacketHandlerFunc(nil)
)
