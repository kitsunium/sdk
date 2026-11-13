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
	// stPeer is a Unix connection accepted on an io_uring direct descriptor
	// whose peer the socket command is reading: not opened yet (#974).
	stPeer
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
	// fCompact puts the slot on its ring's compaction list.
	fCompact
	// fDrained is a connection whose handler the drain has told once.
	fDrained
)

// How a flight leaves on io_uring (connCtl.zc, #969).
const (
	// zcOff: by IORING_OP_SEND.
	zcOff uint8 = iota
	// zcOn: by IORING_OP_SEND_ZC, every send of it.
	zcOn
	// zcHold: no send for now; the last notification of the flight sends its
	// rest by IORING_OP_SEND.
	zcHold
)

// The bits of connCtl.fileBits, io_uring's file state.
const (
	// ffEOF is a read that returned nothing.
	ffEOF uint8 = 1 << iota
	// ffFull is a splice the socket refused for room.
	ffFull
	// ffPoll is the POLLOUT that waits for it.
	ffPoll
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
	// rFile is a file body that ended before its declared length.
	rFile
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
	// heardNow says the handler was handed bytes since the drain last looked
	// at the slot: what they asked for may be written only at OnBatchEnd, so
	// the drain closes the connection no earlier than the next batch (#1385).
	heardNow bool
	errno    int32
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
	// fileFD is the file being sent, the engine's own duplicate; -1 when none.
	fileFD int32
	// pipeR and pipeW are the pipe the file crosses on io_uring, a connection's
	// own, -1 when none; pipeHeld how many bytes the pipe holds.
	pipeR, pipeW int32
	pipeHeld     int32
	// fileOps counts the file's operations in flight on io_uring.
	fileOps uint8
	// fileBits holds ffEOF, ffFull and ffPoll.
	fileBits uint8
	// zc is how the flight leaves (#969): zcOff by plain sends, zcOn every
	// send of it zero-copy, zcHold none for now — zcPend is at its bound —
	// until its notifications came, the rest then sent plainly. zcPend counts
	// its sends whose notification has not come: the flight's chunk is the
	// kernel's until it reaches zero. zcVoid counts the notifications still
	// to come of sends the kernel refused, which pinned nothing (#1396): they
	// hold the slot, never the flight.
	zc     uint8
	zcPend uint8
	zcVoid uint8
	// fileErr is the errno a file operation failed with, zero when none.
	fileErr int32
	// fileOff is the next byte of the file to send, fileLeft how many are left.
	fileOff  int64
	fileLeft int64
}

// sending reports whether the slot holds output, sent or not, or a file.
func (c *connCtl) sending() bool {
	//: bytes, or a file behind them.
	return c.pending() > 0 || c.fileFD >= 0
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
		gen: gen, fd: -1, fileFD: -1, pipeR: -1, pipeW: -1, state: stFree, reason: rNone,
		spill: chunkRef{idx: noChunk}, out: chunkRef{idx: noChunk}, flight: chunkRef{idx: noChunk},
		lruPrev: noLink, lruNext: noLink, dirtyNext: noLink,
	}
}
