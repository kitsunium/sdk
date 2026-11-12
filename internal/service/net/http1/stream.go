package http1

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// openStream writes a Stream answer's head into the batch's output and records
// the connection, or answers 500 when the stream cannot be: a misuse of the
// response, or no handler that speaks the stream port. It reports whether the
// connection stops being read, which is always: a stream is the connection's
// last response.
func (a *LoopHandler) openStream(ref corenet.LoopConnRef, st *loopState, res *corenet.HTTP1ResponseValue) bool {
	f := st.flag(ref)
	off := len(st.out)
	out, err := AppendResponse(st.out, &st.req, res, false)
	//: nothing to stream through, or a head the writer refuses, or an answer
	//: this one would pass.
	if err != nil || res.Defer || res.Pending || a.streamer == nil || len(f.held) > 0 || (res.Upgrade && a.upgrader == nil) {
		st.out = appendInternalError(st.out[:off], res)
		st.items = append(st.items, item{ref: ref, off: off, end: len(st.out), closing: true})
		st.give(res)
		//: refused, and closed with its 500.
		return true
	}
	st.out = out
	st.items = append(st.items, item{ref: ref, off: off, end: len(st.out)})
	st.give(res)
	f.mode = connStreaming
	//: past a 101 the rest of the buffer is the protocol's own bytes, read on.
	stop := !res.Upgrade
	if res.Upgrade {
		f.mode, f.upgradePending = connUpgraded, true
	}
	st.opened = append(st.opened, ref)
	//: whether nothing more of this connection is framed.
	return stop
}

// feedUpgraded hands the bytes after a 101 to the handler, once the 101 has left
// the adapter; bytes that came before the head left wait in the spill for the
// next read, in order, and a client that does not wait for the 101 only delays
// itself.
func (a *LoopHandler) feedUpgraded(c dataCall, buf []byte) (int, stepKind) {
	//: the 101 is still in this batch's output.
	if c.f.upgradePending {
		//: wait.
		return 0, stepWait
	}
	//: as many whole units as the protocol took.
	n := a.upgrader.OnUpgradedData(corenet.HTTP1StreamValue{IO: c.io, Ref: c.ref}, buf)
	//: the rest waits in the spill.
	return n, stepWait
}

// announceStreams tells the handler of each stream the batch opened, now that
// the head has been written.
func (a *LoopHandler) announceStreams(io corenet.LoopIO, st *loopState) {
	//: each new stream, once.
	for _, ref := range st.opened {
		f := st.peek(ref)
		//: closed in the same batch, its flag gone or already given up on.
		if f != nil && (f.mode == connStreaming || f.mode == connUpgraded) && !f.dead {
			f.upgradePending = false
			a.streamer.OnStreamOpen(corenet.HTTP1StreamValue{IO: io, Ref: ref})
		}
	}
	st.opened = st.opened[:0]
}

// OnDrain implements corenet.LoopDrainer: a connection streaming a response is
// told through its HTTP1Streamer; any other is left to the drain.
func (a *LoopHandler) OnDrain(io corenet.LoopIO, ref corenet.LoopConnRef) {
	//: only a stream has something to say.
	if f := a.state(io.Loop()).peek(ref); f != nil && (f.mode == connStreaming || f.mode == connUpgraded) {
		a.streamer.OnStreamDrain(corenet.HTTP1StreamValue{IO: io, Ref: ref})
	}
}
