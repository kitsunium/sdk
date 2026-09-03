// Package net — the received datagram port.
package net

import stdnet "net"

// Packet is one received datagram.
//
// Data is valid only until ServePacket returns: the buffer is recycled straight
// back into the read batch, which is what makes batched datagram reading
// allocation-free. A handler that needs the payload afterwards must copy it.
//
// IFACE-PLUGIN: the concrete type stays unexported in internal/service/net/server.
type Packet interface {
	// Data returns the datagram payload.
	Data() []byte
	// From returns the sender's address.
	From() stdnet.Addr
	// To returns the local address the datagram arrived on.
	To() stdnet.Addr
	// ID returns the monotonic identifier assigned at read time.
	ID() uint64
	// Group returns the name of the listener group that received it.
	Group() string
	// Reply writes a datagram back to the sender. It exists so the common case
	// needs no reference to the underlying connection.
	Reply(b []byte) (n int, err error)
}
