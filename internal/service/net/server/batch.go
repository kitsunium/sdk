package server

import (
	stdnet "net"
	"sync/atomic"
)

const (
	// replyQueue is how many replies one batch may queue before they are sent
	// early to make room.
	replyQueue int = 128
	// replyArena is the room the queued replies' bytes share, copied out of the
	// handler's buffer so it may reuse it as soon as Reply returns.
	replyArena int = 256 << 10
)

// datagram is one slot in a read batch: a reusable buffer plus what the last
// read put in it. Slots are allocated once per read loop and reused for the
// life of the socket, which is what keeps datagram serving allocation-free.
//
// One slot is one message of the kernel, and with UDP GRO a message is several
// datagrams of one sender laid end to end: seg is then their size, every one
// but the last exactly seg bytes long, and the read loop hands each to the
// handler as its own Packet (ADR 0192).
type datagram struct {
	// buf is the slot's reusable payload buffer.
	buf []byte
	// n is how many bytes the last read placed in buf.
	n int
	// seg is the size of each datagram of a coalesced message, as the UDP_GRO
	// control message reported it; zero when the message is one datagram.
	seg int
	// cut marks a message the kernel cut short — its payload past the buffer
	// on a coalesced read (MSG_TRUNC) or its control data past the room left
	// for it (MSG_CTRUNC): what survives cannot be split into whole datagrams,
	// so the read loop drops it and counts it in State.
	cut bool
	// addr is the sender of the last read.
	addr stdnet.Addr
}

// payload returns the bytes of the last read.
func (d *datagram) payload() []byte {
	//: only the prefix the read actually filled is meaningful.
	return d.buf[:d.n]
}

// segment returns the i-th datagram of the slot's message and whether there is
// one: the whole payload once for a message of one datagram, then seg-sized
// pieces with a shorter last one for a coalesced message.
func (d *datagram) segment(i int) (piece []byte, ok bool) {
	//: one datagram: the message is it, once.
	if d.seg <= 0 || d.seg >= d.n {
		//: the whole payload at the first index, nothing after it.
		return d.payload(), i == 0
	}
	off := i * d.seg
	//: past the message's end.
	if off >= d.n {
		//: no datagram at this index.
		return nil, false
	}
	//: the last datagram of a coalesced message may be shorter than seg.
	return d.buf[off:min(off+d.seg, d.n)], true
}

// count returns how many datagrams the slot's message carries.
func (d *datagram) count() int {
	//: no segment size to split by: the message is one datagram.
	if d.seg <= 0 || d.seg >= d.n {
		//: one datagram.
		return 1
	}
	//: whole segments, and a shorter last one.
	return (d.n + d.seg - 1) / d.seg
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
	// slotSize is the size newSlots is given for a group whose ceiling is
	// ceiling: the ceiling itself, or a whole coalesced message when the
	// source reads UDP GRO.
	slotSize(ceiling int) int
}

// datagramSink sends the replies a read batch's handlers make.
//
// By default every reply is a WriteTo, one datagram a call, its error the
// caller's (portableSink). Under BatchReplies the replies made while a batch is
// served are queued and leave together when it ends: on Linux one sendmmsg,
// consecutive replies to one peer of one size as one UDP GSO message
// (multiWriter); elsewhere, and on a socket sendmmsg cannot address, one WriteTo
// each (queuedSink) — darwin sends one datagram a call (ADR 0192).
//
// IFACE-PLUGIN: implementations are selected by build tag and stay unexported.
type datagramSink interface {
	// begin opens a batch: replies made until flush may be queued.
	begin()
	// reply sends b to to, or queues it until flush while a batch is open.
	reply(b []byte, to stdnet.Addr) (n int, err error)
	// flush sends what the batch queued and closes it.
	flush()
}

// datagramCounters are what the batched datagram paths drop, reported in
// State's Datagrams. One set per server, shared by every socket's reader and
// sink.
type datagramCounters struct {
	// truncated counts messages dropped for MSG_TRUNC on a coalesced read or
	// MSG_CTRUNC on any.
	truncated atomic.Uint64
	// gsoOversized counts GSO messages the kernel refused with EMSGSIZE.
	gsoOversized atomic.Uint64
	// replyFailures counts queued replies that did not leave.
	replyFailures atomic.Uint64
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
