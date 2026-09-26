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

// To implements corenet.Packet. A packet whose read loop has ended has no
// socket left to ask, and answers nil.
func (p *packet) To() stdnet.Addr {
	//: the loop reset it: the socket is gone, not waiting to be asked.
	if p.conn == nil {
		//: no local address to report.
		return nil
	}
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
//
// The port bounds only Data to the handler's call, so a handler may keep the
// Packet and reply later. Once the read loop has ended and reset it, the reply
// is net.ErrClosed — what writing to the closed socket would have answered —
// rather than a nil dereference.
func (p *packet) Reply(b []byte) (n int, err error) {
	//: the loop reset it: the socket it arrived on is closed.
	if p.conn == nil {
		//: the closed-socket answer, without a socket to ask.
		return 0, stdnet.ErrClosed
	}
	//: write straight back to the address the datagram came from.
	return p.conn.WriteTo(b, p.from)
}

// reset clears the datagram when its read loop ends. It must drop every
// reference: a handler that kept the Packet past its call would otherwise pin
// the closed socket and the last read buffer for as long as it holds it.
func (p *packet) reset() {
	p.data = nil
	p.from = nil
	p.conn = nil
	p.id = 0
	p.group = ""
}
