// Package server — the pooled datagram.
package server

import (
	stdnet "net"
)

// packet is the domain's view of one received datagram.
//
// Its payload points into the read buffer that produced it, which is what makes
// batched reading allocation-free — and is why the port documents that Data is
// valid only until ServePacket returns.
type packet struct {
	// data is the payload, a subslice of the owning read buffer.
	data []byte
	// from is the sender's address.
	from stdnet.Addr
	// conn is the socket the datagram arrived on, used by Reply.
	conn stdnet.PacketConn
	// id is the monotonic identifier assigned at read time.
	id uint64
	// group names the listener group that received it.
	group string
}

// Data implements corenet.Packet.
func (p *packet) Data() []byte {
	//: a subslice of the read buffer; valid only until ServePacket returns.
	return p.data
}

// From implements corenet.Packet.
func (p *packet) From() stdnet.Addr {
	//: the sender, as reported by the read.
	return p.from
}

// To implements corenet.Packet.
func (p *packet) To() stdnet.Addr {
	//: the local address the datagram arrived on.
	return p.conn.LocalAddr()
}

// ID implements corenet.Packet.
func (p *packet) ID() uint64 {
	//: assigned once at read time.
	return p.id
}

// Group implements corenet.Packet.
func (p *packet) Group() string {
	//: the owning group's name.
	return p.group
}

// Reply implements corenet.Packet.
//
// It exists so the overwhelmingly common case — answer the sender — needs no
// reference to the underlying socket, which the handler has no other way to
// reach and should not have to thread through itself.
func (p *packet) Reply(b []byte) (n int, err error) {
	//: write straight back to the address the datagram came from.
	return p.conn.WriteTo(b, p.from)
}

// reset clears the datagram so the pool can hand it out again. It must drop
// every reference or a pooled entry would pin a closed socket and a read buffer.
func (p *packet) reset() {
	p.data = nil
	p.from = nil
	p.conn = nil
	p.id = 0
	p.group = ""
}
