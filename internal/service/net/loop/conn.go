package loop

// The states of a connection slot. A slot returns to stFree only once its
// backend released the descriptor and every operation still in flight, its
// generation moved on.
const (
	// stFree is a slot on the free list.
	stFree uint8 = iota
	// stOpen serves its connection.
	stOpen
	// stClosing was asked to close and still has output to send.
	stClosing
	// stDead has called OnClose and waits for its backend to release the
	// descriptor and every operation still in flight.
	stDead
)

// The flags of a connection slot.
const (
	// fPressure is the soft pressure set at accept.
	fPressure uint8 = 1 << iota
	// fWantWritable asks for OnWritable once the output falls under the
	// low-water mark.
	fWantWritable
	// fDirty puts the slot on its ring's dirty list.
	fDirty
	// fPeerDone is a peer that sent its last byte.
	fPeerDone
	// fSending is output the backend holds: EPOLLOUT armed, or a send in
	// flight.
	fSending
	// fHeard is a peer that sent at least one byte: the drain closes a
	// connection that has not only once it stayed silent for drainQuiet.
	fHeard
)

// The reasons a connection is closed, mapped to the preallocated error
// OnClose receives (closeErr).
const (
	// rNone is a clean close, by the handler or the peer: OnClose receives
	// nil.
	rNone uint8 = iota
	// rSpill is unconsumed input past SpillCap, or the slab's budget spent.
	rSpill
	// rIdle is no traffic for IdleTimeout.
	rIdle
	// rDrain is the group's drain or stop.
	rDrain
	// rErrno is a socket failure; the slot's errno says which.
	rErrno
	// rEvict is an idle connection evicted under hard pressure.
	rEvict
)

// connCtl is one connection slot's control state. It holds no pointer, so the
// table of them is a pointer-free Go slice the race detector sees and the
// collector does not scan (ADR 0186 §D7); every byte it names lives in the
// slab.
type connCtl struct {
	// gen is the slot's generation, never zero once the slot has served.
	gen uint32
	// state and flags are the slot's state and its flags.
	state uint8
	flags uint8
	// reason is why the connection closed; errno its errno when it is rErrno.
	reason uint8
	errno  int32
	// fd is the connection's descriptor: a process descriptor on epoll, a
	// direct-descriptor index on io_uring.
	fd int32
	// ops counts operations in flight on io_uring; the slot is released only
	// at zero.
	ops int32
	// spill holds the unconsumed input; out the written, unsent output;
	// flight the output the kernel is sending.
	spill  chunkRef
	out    chunkRef
	flight chunkRef
	// lastIO is the tick of the last byte received or sent.
	lastIO int64
	// deadline is the handler's deadline tick, zero when none.
	deadline int64
	// lruPrev and lruNext link the ring's open connections, least recent first.
	lruPrev int32
	lruNext int32
	// dirtyNext links the ring's dirty list.
	dirtyNext int32
}

// pending is how many output bytes the slot holds, sent or not.
func (c *connCtl) pending() int {
	//: both halves of the double buffer.
	return int(c.out.len-c.out.off) + int(c.flight.len-c.flight.off)
}

// emptyConn is a slot's state on the free list.
func emptyConn(gen uint32) connCtl {
	//: every chunk released, every link cut.
	return connCtl{
		gen: gen, fd: -1, state: stFree, reason: rNone,
		spill: chunkRef{idx: noChunk}, out: chunkRef{idx: noChunk}, flight: chunkRef{idx: noChunk},
		lruPrev: noLink, lruNext: noLink, dirtyNext: noLink,
	}
}
