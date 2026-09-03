// Package server — the datagram batch-read abstraction.
package server

import (
	stdnet "net"
)

// datagram is one slot in a read batch: a reusable buffer plus what the last
// read put in it. Slots are allocated once per read loop and reused for the
// life of the socket, which is what keeps datagram serving allocation-free.
type datagram struct {
	// buf is the slot's reusable payload buffer.
	buf []byte
	// n is how many bytes the last read placed in buf.
	n int
	// addr is the sender of the last read.
	addr stdnet.Addr
}

// payload returns the bytes of the last read.
func (d *datagram) payload() []byte {
	//: only the prefix the read actually filled is meaningful.
	return d.buf[:d.n]
}

// datagramSource collects one or more datagrams per call.
//
// The interface exists so the read loop is written once against "give me up to
// N datagrams", and the platform decides how many syscalls that costs. On Linux
// it is one recvmmsg; elsewhere it is a ReadFrom per datagram. The loop above it
// does not change either way — which is why the type is named for the role it
// plays rather than for its single method.
//
// IFACE-PLUGIN: implementations are selected by build tag and stay unexported.
type datagramSource interface {
	// readBatch fills slots and returns how many were filled. It returns at
	// least one datagram or an error.
	readBatch(slots []datagram) (n int, err error)
}

// newSlots allocates a read batch sized for the group.
//
// Each buffer is ONE BYTE past the group's ceiling, and that probe byte is what
// makes an oversized datagram detectable at all. The kernel copies as much as
// the buffer holds and discards the remainder, so a buffer sized exactly at the
// ceiling cannot tell a datagram that just fits from one that was cut down to
// fit — both report the same length. Reading one byte further makes the two
// distinguishable, which is the same reason the outbound body ceiling reads one
// byte past its own limit.
func newSlots(count, size int) []datagram {
	//: indexed, never appended to — the length IS the batch size.
	slots := make([]datagram, count)
	//: each slot owns its buffer for the life of the read loop, so a read
	//: allocates nothing at all once the loop is running.
	for i := range slots {
		slots[i].buf = make([]byte, size+1)
	}
	//: ready to be handed to the platform reader.
	return slots
}
