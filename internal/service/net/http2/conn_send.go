package http2

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// Headers appends a stream's response HEADERS, split over CONTINUATIONs at the
// frame size the peer accepts, and closes our half when endStream. A stream
// that already ended writes nothing. A response AppendHeaders refuses returns
// its refusal, nothing appended.
func (l *Lane) Headers(c *Conn, stream uint32, res *corenet.HTTP1ResponseValue, endStream bool, dst []byte) ([]byte, error) {
	idx, _ := l.find(c, stream)
	//: a stream the peer reset takes no answer.
	if idx == noIndex {
		return dst, nil
	}
	out, err := NewEncoder(int(c.peerMaxFrame)).AppendHeaders(dst, stream, res, endStream)
	//: a response the writer refuses is the adapter's to answer.
	if err != nil {
		return dst, err
	}
	//: the headers ended the response: the stream may be done.
	if endStream {
		f := feedRun{l: l, c: c, dst: out}
		l.streams[idx].set(streamLocalClosed)
		f.finishIfDone(idx)
		out = f.dst
	}
	return out, nil
}

// Send appends DATA frames carrying body for stream, as many as the peer's
// windows and frame size allow, and closes our half when endStream once all
// of it is out. What the windows do not allow is held for the connection and
// resumed when WINDOW_UPDATE opens them, bounded by the lane's holdCap: past
// it the connection ends with GOAWAY(ENHANCE_YOUR_CALM). Nothing is written for
// a stream that ended.
func (l *Lane) Send(c *Conn, stream uint32, body, dst []byte, endStream bool) []byte {
	idx, _ := l.find(c, stream)
	//: a stream the peer reset takes no bytes.
	if idx == noIndex {
		return dst
	}
	f := feedRun{l: l, c: c, dst: dst}
	f.sendNow(idx, body, endStream)
	return f.dst
}

// sendNow writes what the windows allow of p on stream idx, and holds the rest.
func (f *feedRun) sendNow(idx int32, p []byte, end bool) {
	st := &f.l.streams[idx]
	n := f.appendData(st, p, end)
	//: everything left: the stream's held range is appended to.
	if n < len(p) {
		f.hold(idx, p[n:], end)
		return
	}
	//: the whole body left, and it was the last of the stream.
	if end {
		st.set(streamLocalClosed)
		f.finishIfDone(idx)
	}
}

// appendData writes DATA frames for p, bounded by both windows and the frame
// size; a stream with no window, or p empty, writes at most one empty DATA
// that carries END_STREAM.
func (f *feedRun) appendData(st *streamState, p []byte, end bool) int {
	n := min(len(p), int(max(st.sendWindow, 0)), int(max(f.c.sendWindow, 0)))
	sent := 0
	maxFrame := int(f.c.peerMaxFrame)
	//: every whole frame of what the windows allow.
	for sent < n {
		size := min(n-sent, maxFrame)
		flags := corehttp2.FrameFlags(0)
		//: END_STREAM goes with the frame that carries the last byte.
		if end && sent+size == len(p) {
			flags = corehttp2.FlagEndStream
		}
		f.dst = AppendFrameHeader(f.dst, corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: flags, StreamID: st.id, Length: size})
		f.dst = append(f.dst, p[sent:sent+size]...)
		sent += size
	}
	st.sendWindow -= int32(n)
	f.c.sendWindow -= int32(n)
	//: nothing to carry: END_STREAM alone, when the whole body had already left.
	if end && len(p) == 0 {
		f.dst = AppendFrameHeader(f.dst, corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: corehttp2.FlagEndStream, StreamID: st.id})
	}
	return n
}

// hold copies what the windows did not allow into the connection's buffer,
// bounded by holdCap; END_STREAM stays with the held bytes.
func (f *feedRun) hold(idx int32, p []byte, end bool) {
	st := &f.l.streams[idx]
	//: past the bound the connection ends; ADR 0194 §D3's bound on held answers.
	if int(f.c.held)+len(p) > int(f.l.holdCap) {
		f.connError(corehttp2.ErrCodeEnhanceYourCalm)
		return
	}
	h := f.l.holdOf(f.c)
	//: a stream that already holds bytes keeps them contiguous: one not at the
	//: buffer's end is moved there first, its old range now dead.
	if st.holdLen > 0 && int(st.holdOff+st.holdLen) != len(h.buf) {
		h.dead += st.holdLen
		h.buf = append(h.buf, h.buf[st.holdOff:st.holdOff+st.holdLen]...)
		st.holdOff = int32(len(h.buf)) - st.holdLen
	}
	//: a stream with nothing held starts a new range at the buffer's end.
	if st.holdLen == 0 {
		st.holdOff = int32(len(h.buf))
	}
	h.buf = append(h.buf, p...)
	st.holdLen += int32(len(p))
	f.c.held += int32(len(p))
	//: END_STREAM waits for the last of the held bytes.
	if end {
		st.set(streamHoldEnd)
	}
}

// holdOf returns c's hold buffer, taking one from the lane on first use.
func (l *Lane) holdOf(c *Conn) *holdBuffer {
	//: the connection's buffer is taken on first need, never before.
	if c.hold == noIndex {
		c.hold = l.takeHold()
	}
	return &l.holds[c.hold]
}

// takeHold returns a free buffer, growing the arena when none is free.
func (l *Lane) takeHold() int32 {
	//: the free list reuses a buffer before another is allocated.
	if n := len(l.freeHolds); n > 0 {
		id := l.freeHolds[n-1]
		l.freeHolds = l.freeHolds[:n-1]
		return id
	}
	l.holds = append(l.holds, holdBuffer{})
	return int32(len(l.holds) - 1)
}

// flushHolds sends whatever the windows now allow of every stream's held
// bytes, in the order the streams opened (newest first); called from Feed
// after a SETTINGS or WINDOW_UPDATE. Nothing is copied from the hold buffer
// until a compaction asks for it.
func (l *Lane) flushHolds(f *feedRun) {
	c := f.c
	//: nothing held: every WINDOW_UPDATE's cheap path.
	if c.held == 0 {
		return
	}
	//: every stream, newest first; a finished stream unlinks itself.
	for idx := c.head; idx != noIndex && c.sendWindow > 0; {
		next := l.streams[idx].next
		l.flushStream(f, idx)
		idx = next
	}
	l.compactHold(c)
}

// flushStream sends what the windows allow of one stream's held bytes.
func (l *Lane) flushStream(f *feedRun, idx int32) {
	st := &l.streams[idx]
	//: the stream holds nothing, or has no window.
	if st.holdLen == 0 || st.sendWindow <= 0 {
		return
	}
	buf := l.holds[f.c.hold].buf
	p := buf[st.holdOff : st.holdOff+st.holdLen]
	end := st.has(streamHoldEnd)
	n := f.appendData(st, p, end)
	st.holdOff += int32(n)
	st.holdLen -= int32(n)
	f.c.held -= int32(n)
	l.holds[f.c.hold].dead += int32(n)
	//: the last held byte has gone, and END_STREAM with it.
	if st.holdLen == 0 && end {
		st.set(streamLocalClosed)
		f.finishIfDone(idx)
	}
}

// compactHold reclaims the buffer's dead bytes: when nothing is held any
// more it goes back to the lane, and otherwise once more than half of it is
// dead the live ranges are moved down.
func (l *Lane) compactHold(c *Conn) {
	//: no buffer: nothing to reclaim.
	if c.hold == noIndex {
		return
	}
	//: everything left: a connection with nothing held keeps no buffer.
	if c.held == 0 {
		l.releaseHold(c)
		return
	}
	h := &l.holds[c.hold]
	//: amortised: only once more than half of the buffer is dead.
	if int(h.dead)*2 > len(h.buf) {
		l.moveHoldDown(c, h)
	}
}

// moveHoldDown moves every stream's held range to the front of its buffer, in
// offset order, so no byte is overwritten before it is copied.
func (l *Lane) moveHoldDown(c *Conn, h *holdBuffer) {
	write := int32(0)
	//: at most MaxStreams ranges, each smallest offset first by a scan.
	for {
		idx := l.lowestHold(c, write)
		//: no range left to move.
		if idx == noIndex {
			break
		}
		st := &l.streams[idx]
		copy(h.buf[write:], h.buf[st.holdOff:st.holdOff+st.holdLen])
		st.holdOff = write
		write += st.holdLen
	}
	h.buf = h.buf[:write]
	h.dead = 0
}

// lowestHold returns the holding stream with the lowest offset at or past
// from: the next range to compact, one scan of the list per range.
func (l *Lane) lowestHold(c *Conn, from int32) int32 {
	best := noIndex
	//: at most MaxStreams.
	for idx := c.head; idx != noIndex; idx = l.streams[idx].next {
		st := &l.streams[idx]
		//: a stream below from has moved already, and one holding nothing has no range.
		if st.holdLen > 0 && st.holdOff >= from && (best == noIndex || st.holdOff < l.streams[best].holdOff) {
			best = idx
		}
	}
	return best
}

// Consume records n body bytes of stream the handler has consumed, on the
// stream if it is still open and always on the connection. A stream whose
// peer half closed takes no credit: nothing more will arrive on it.
func (l *Lane) Consume(c *Conn, stream uint32, n int) {
	c.peerCredit += int32(n)
	idx, _ := l.find(c, stream)
	//: a closed stream has no window to refill; the connection still does.
	if idx == noIndex || l.streams[idx].has(streamRemoteClosed) {
		return
	}
	l.streams[idx].peerCredit += int32(n)
}

// AppendCredits appends at most one WINDOW_UPDATE per stream and one per
// connection, each crediting only bytes the handler consumed: the batch's
// credits coalesce. A credit under a quarter of its window waits: a window
// refilled a little at a time spends more frames than it saves.
func (l *Lane) AppendCredits(c *Conn, dst []byte) []byte {
	//: every open stream's accumulated credit.
	for idx := c.head; idx != noIndex; idx = l.streams[idx].next {
		st := &l.streams[idx]
		//: a stream's credit waits for its fraction of the window.
		if st.peerCredit >= int32(l.limits.ReceivePerStream)/creditDivisor {
			dst = AppendWindowUpdate(dst, st.id, uint32(st.peerCredit))
			st.peerWindow += st.peerCredit
			st.peerCredit = 0
		}
	}
	//: the connection's credit has its own fraction.
	if c.peerCredit >= int32(l.limits.ReceivePerConn)/creditDivisor {
		dst = AppendWindowUpdate(dst, 0, uint32(c.peerCredit))
		c.peerWindow += c.peerCredit
		c.peerCredit = 0
	}
	return dst
}

// GoAway appends a GOAWAY with code naming the last stream the connection
// opened, and admits no new stream afterwards: the h2 reading of
// Connection: close, ADR 0221. A second call writes nothing. The connection
// closes when its streams end (Done).
func (l *Lane) GoAway(c *Conn, code corehttp2.ErrCode, dst []byte) []byte {
	//: one GOAWAY a connection.
	if c.has(flagGoAway) {
		return dst
	}
	c.set(flagGoAway)
	return AppendGoAway(dst, c.lastOpen, code)
}

// Abort ends the connection with a GOAWAY carrying code: the answer to a
// field block Decode refused.
func (*Lane) Abort(c *Conn, code corehttp2.ErrCode, dst []byte) []byte {
	c.set(flagClosing)
	return AppendGoAway(dst, c.lastOpen, code)
}

// ResetStream appends RST_STREAM(code) for stream and closes it, so a further
// answer is not written: the answer to a malformed request.
func (l *Lane) ResetStream(c *Conn, stream uint32, code corehttp2.ErrCode, dst []byte) []byte {
	idx, _ := l.find(c, stream)
	//: a stream already gone needs no reset.
	if idx == noIndex {
		return dst
	}
	dst = AppendRSTStream(dst, stream, code)
	l.closeStream(c, idx)
	return dst
}

// OpenStreams reports how many streams c holds open.
func (*Lane) OpenStreams(c *Conn) int {
	return int(c.open)
}
