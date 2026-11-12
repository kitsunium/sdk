package http2

import (
	"bytes"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// feedRun is one Feed's cursor, on its stack.
type feedRun struct {
	// l is the lane.
	l *Lane
	// c is the connection.
	c *Conn
	// buf is the input.
	buf []byte
	// dst receives the connection's own answers.
	dst []byte
	// h is the current frame's header.
	h corehttp2.FrameValue
	// payload is the current frame's payload.
	payload []byte
}

// Feed consumes whole frames from the start of buf and returns how many
// bytes it took: a partial frame stays for the next call, the loop's spill
// holding it. It appends to dst what the connection answers — SETTINGS, its
// ACK, PING answers, RST_STREAM, GOAWAY, held DATA the peer's credit releases —
// and reports the rest as Events. A connection the peer broke ends with a
// GOAWAY and Closing reports it. It allocates nothing once the lane's arenas
// have grown to its batches.
func (l *Lane) Feed(c *Conn, buf, dst []byte) (int, []byte) {
	l.events = l.events[:0]
	f := feedRun{l: l, c: c, buf: buf, dst: dst}
	f.startBatch()
	used, ready := f.preface()
	//: the preface decides: wait for the rest of it, or the connection ended.
	if !ready {
		return used, f.dst
	}
	//: a connection that ended takes no more of the input.
	for used < len(buf) && !c.has(flagClosing) {
		n := f.frame(buf[used:])
		//: an incomplete frame stays, for the next bytes.
		if n == 0 {
			break
		}
		used += n
	}
	//: after an error nothing more is framed: the rest of the input is dropped.
	if c.has(flagClosing) {
		return len(buf), f.dst
	}
	return used, f.dst
}

// startBatch resets the connection's control budget for a new batch.
func (f *feedRun) startBatch() {
	//: the counter is per batch: a stale epoch starts it over.
	if f.c.epoch != f.l.epoch {
		f.c.epoch = f.l.epoch
		f.c.control = 0
	}
}

// preface reads the client connection preface, RFC 9113 §3.4, and writes the
// server's: its SETTINGS and the connection's window raised to the bound. It
// reports the bytes it took and whether frames may be read: once the preface
// was consumed earlier, zero bytes and true.
func (f *feedRun) preface() (int, bool) {
	//: the preface was read once.
	if f.c.has(flagPreface) {
		return 0, true
	}
	n := min(len(f.buf), clientPrefaceLen)
	//: the bytes seen so far must be the preface's.
	if !bytes.Equal(f.buf[:n], clientPrefaceBytes[:n]) {
		f.connError(corehttp2.ErrCodeProtocol)
		return len(f.buf), false
	}
	//: the preface is not all there yet.
	if n < clientPrefaceLen {
		return 0, false
	}
	f.c.set(flagPreface)
	f.dst = appendServerPreface(f.dst, f.l)
	//: the WINDOW_UPDATE just written raises what the peer may send on the connection.
	f.c.peerWindow = int32(f.l.limits.ReceivePerConn)
	return clientPrefaceLen, true
}

// appendServerPreface writes the SETTINGS the server opens with: the bounds
// that differ from the protocol's defaults, and the connection window raised.
func appendServerPreface(dst []byte, l *Lane) []byte {
	settings := [...]corehttp2.SettingValue{
		{ID: corehttp2.SettingMaxConcurrentStreams, Val: uint32(l.limits.MaxStreams)},
		{ID: corehttp2.SettingInitialWindowSize, Val: uint32(l.limits.ReceivePerStream)},
		{ID: corehttp2.SettingMaxHeaderListSize, Val: uint32(l.limits.MaxHeaderList)},
		{ID: corehttp2.SettingHeaderTableSize, Val: uint32(l.limits.HeaderTable)},
		{ID: corehttp2.SettingMaxFrameSize, Val: uint32(l.limits.MaxFrame)},
	}
	dst = AppendSettings(dst, settings[:])
	//: the connection window starts at the protocol's 65535 (§6.9.2) and is raised here.
	return AppendWindowUpdate(dst, 0, uint32(l.limits.ReceivePerConn-corehttp2.DefaultWindow))
}

// frame consumes one frame, or one header sequence, from the input's front.
// It returns the bytes taken, 0 while the frame is not whole.
func (f *feedRun) frame(in []byte) int {
	h, n, code := ParseFrameHeader(in, f.l.limits.MaxFrame)
	//: too few bytes for the header.
	if n == 0 {
		return 0
	}
	//: a refused header ends the connection; length and padding are its alone.
	if code != corehttp2.ErrCodeNoError {
		f.connError(code)
		return 0
	}
	//: the frame is held until it is whole, in the loop's spill.
	if len(in) < n+h.Length {
		return 0
	}
	f.h, f.payload = h, in[n:n+h.Length]
	//: the first frame after the preface is SETTINGS, §3.4.
	if !f.c.has(flagSettings) && (h.Type != corehttp2.FrameSettings || h.Flags&corehttp2.FlagAck != 0) {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: a frame of a type this package does not name is an extension, §4.1.
	if h.Type > frameKnownMax {
		f.spendControl()
		return n + h.Length
	}
	//: the dispatch is direct, in two halves of the type space: an indirect call
	//: would move this cursor to the heap, one allocation per Feed.
	if h.Type <= corehttp2.FrameSettings {
		return f.dispatchLow(in)
	}
	return f.dispatchHigh()
}

// dispatchLow handles the frame types DATA through SETTINGS.
func (f *feedRun) dispatchLow(in []byte) int {
	//: one handler per type.
	switch f.h.Type {
	case corehttp2.FrameData:
		return f.dataFrame()
	case corehttp2.FrameHeaders:
		return f.headersFrame(in)
	case corehttp2.FramePriority:
		return f.priorityFrame()
	case corehttp2.FrameRSTStream:
		return f.rstFrame()
	default:
		return f.settingsFrame()
	}
}

// dispatchHigh handles the frame types PUSH_PROMISE through CONTINUATION.
func (f *feedRun) dispatchHigh() int {
	//: one handler per type.
	switch f.h.Type {
	case corehttp2.FramePushPromise:
		return f.pushFrame()
	case corehttp2.FramePing:
		return f.pingFrame()
	case corehttp2.FrameGoAway:
		return f.goAwayFrame()
	case corehttp2.FrameWindowUpdate:
		return f.windowFrame()
	default:
		return f.continuationFrame()
	}
}

// frameLen returns the whole frame's length on the wire.
func (f *feedRun) frameLen() int {
	return corehttp2.FrameHeaderLen + f.h.Length
}

// spendControl charges one frame to the connection's batch budget; a frame
// past it ends the connection, ENHANCE_YOUR_CALM, RFC 9113 §10.5: a flood of
// frames that take no work is refused before it costs output.
func (f *feedRun) spendControl() {
	f.c.control++
	//: the budget's past: this connection is flooding.
	if f.c.control > controlPerBatch {
		f.connError(corehttp2.ErrCodeEnhanceYourCalm)
	}
}

// connError ends the connection: GOAWAY with code, and nothing more is read.
// A second error of the same input writes nothing.
func (f *feedRun) connError(code corehttp2.ErrCode) {
	//: the first error is the one reported.
	if f.c.has(flagClosing) {
		return
	}
	f.c.set(flagClosing)
	f.l.countErr(code)
	f.dst = AppendGoAway(f.dst, f.c.lastOpen, code)
}

// streamError ends one stream: RST_STREAM with code, and its event.
func (f *feedRun) streamError(idx int32, code corehttp2.ErrCode) {
	stream := f.l.streams[idx].id
	f.l.countErr(code)
	f.dst = AppendRSTStream(f.dst, stream, code)
	f.l.closeStream(f.c, idx)
	f.l.events = append(f.l.events, Event{Kind: EventReset, Stream: stream, Code: code})
}

// emit reports an event for the adapter.
func (f *feedRun) emit(ev Event) {
	f.l.events = append(f.l.events, ev)
}

// find returns the arena index of stream id on c and its predecessor, or
// noIndex: a walk of the list, newest first, stopped at the first older id
// (ids only grow, so nothing older can match past it).
func (l *Lane) find(c *Conn, id uint32) (idx, prev int32) {
	prev = noIndex
	//: at most MaxStreams steps.
	for idx = c.head; idx != noIndex; idx = l.streams[idx].next {
		//: found.
		if l.streams[idx].id == id {
			return idx, prev
		}
		//: every later entry is older than id.
		if l.streams[idx].id < id {
			return noIndex, prev
		}
		prev = idx
	}
	return noIndex, prev
}

// newStream opens stream id at the head of the list, newest first, windows
// taken from the peer's settings and the receive bound.
func (l *Lane) newStream(c *Conn, id uint32) int32 {
	idx := l.freeStream
	//: the free list reuses a slot before the arena grows.
	if idx != noIndex {
		l.freeStream = l.streams[idx].next
	} else {
		l.streams = append(l.streams, streamState{})
		idx = int32(len(l.streams) - 1)
	}
	l.streams[idx] = streamState{
		id:         id,
		next:       c.head,
		peerWindow: int32(l.limits.ReceivePerStream),
		sendWindow: c.initialSend,
	}
	c.head = idx
	c.open++
	return idx
}

// closeStream unlinks stream idx from c and frees it.
func (l *Lane) closeStream(c *Conn, idx int32) {
	id := l.streams[idx].id
	_, prev := l.find(c, id)
	st := &l.streams[idx]
	//: the head of the list has no predecessor to relink.
	if prev == noIndex {
		c.head = st.next
	} else {
		l.streams[prev].next = st.next
	}
	c.open--
	c.held -= st.holdLen
	l.freeStreamSlot(idx)
}

// freeStreamSlot returns a stream slot to the free list.
func (l *Lane) freeStreamSlot(idx int32) {
	l.streams[idx].next = l.freeStream
	l.freeStream = idx
}

// releaseHold gives a connection's hold buffer back to the lane.
func (l *Lane) releaseHold(c *Conn) {
	//: nothing is held.
	if c.hold == noIndex {
		return
	}
	//: a big buffer goes back to the heap rather than the free list.
	if cap(l.holds[c.hold].buf) > holdKeepMax {
		l.holds[c.hold].buf = nil
	}
	l.holds[c.hold].buf = l.holds[c.hold].buf[:0]
	l.holds[c.hold].dead = 0
	l.freeHolds = append(l.freeHolds, c.hold)
	c.hold = noIndex
}

// finishIfDone closes a stream once both halves are done: its answer fully
// written (END_STREAM sent, nothing held) and the peer's half closed. A
// response that ended first while the body is still arriving is ended with
// RST_STREAM(NO_ERROR), RFC 9113 §8.1, so the rest is discarded.
func (f *feedRun) finishIfDone(idx int32) {
	st := &f.l.streams[idx]
	//: not answered yet, or bytes still held.
	if !st.has(streamLocalClosed) || st.holdLen > 0 {
		return
	}
	//: the peer is still sending: the rest is discarded, no error.
	if !st.has(streamRemoteClosed) {
		f.dst = AppendRSTStream(f.dst, st.id, corehttp2.ErrCodeNoError)
	}
	f.refundReset()
	f.l.closeStream(f.c, idx)
}

// refundReset returns one unit of the Rapid Reset budget, a stream having
// finished its life normally.
func (f *feedRun) refundReset() {
	limit := resetBudgetFactor * int32(f.l.limits.MaxStreams)
	//: at most the starting budget.
	if f.c.resets < limit {
		f.c.resets++
	}
}
