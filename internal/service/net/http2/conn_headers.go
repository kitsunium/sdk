package http2

import (
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// headerSequence is a HEADERS frame and its CONTINUATIONs read together.
type headerSequence struct {
	// total is the sequence's bytes on the wire.
	total int
	// block is the assembled field block.
	block []byte
	// endStream reports END_STREAM on the HEADERS frame.
	endStream bool
}

// headersFrame handles a HEADERS frame, RFC 9113 §6.2, the whole sequence of
// frames that carries one field block being read at once.
func (f *feedRun) headersFrame(in []byte) int {
	//: HEADERS belongs to a stream, §6.2.
	if f.h.StreamID == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: a client's streams are odd-numbered, §5.1.1.
	if f.h.StreamID%2 == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	seq, ok := f.readSequence(in)
	//: a sequence not yet whole, or refused: either way nothing is consumed.
	if !ok || f.c.has(flagClosing) {
		return 0
	}
	f.c.scanFrames, f.c.scanOff, f.c.scanBlock = 0, 0, 0
	f.onHeaderBlock(&seq)
	return seq.total
}

// readSequence walks the frames carrying one field block, RFC 9113 §6.10:
// HEADERS then CONTINUATIONs of its stream, none interleaved, the fragment
// bytes and the frames bounded before anything is copied. Progress is kept in
// the Conn so a block trickled a byte at a time is walked once, not re-read.
func (f *feedRun) readSequence(in []byte) (headerSequence, bool) {
	first := f.fragmentOf(f.h, f.payload)
	endStream := f.h.Flags&corehttp2.FlagEndStream != 0
	//: the whole block fits the HEADERS frame: nothing to assemble.
	if f.h.Flags&corehttp2.FlagEndHeaders != 0 {
		return headerSequence{total: f.frameLen(), block: first, endStream: endStream}, true
	}
	off, blockLen := f.resumeScan(in, int32(len(first)))
	//: frames this connection's earlier calls already found good are skipped.
	for {
		h, n, ok := f.nextContinuation(in[off:])
		//: not whole, or refused: the progress so far waits.
		if !ok {
			f.c.scanOff, f.c.scanBlock = off, blockLen
			return headerSequence{}, false
		}
		off += int32(n)
		blockLen += int32(h.Length)
		//: the sequence's bytes and frames are bounded.
		if !f.withinBlockBound(blockLen) {
			return headerSequence{}, false
		}
		f.c.scanFrames++
		//: the block's last frame.
		if h.Flags&corehttp2.FlagEndHeaders != 0 {
			return f.joinSequence(in, first, int(off), endStream), true
		}
	}
}

// resumeScan returns where to resume walking the block's CONTINUATIONs: past
// the frames an earlier call found good, or from the start when the input no
// longer holds them.
func (f *feedRun) resumeScan(in []byte, firstLen int32) (off, blockLen int32) {
	frameEnd := int32(f.frameLen())
	//: the same bytes are handed back, longer: any progress beyond them is stale.
	if f.c.scanOff > frameEnd && int(f.c.scanOff) <= len(in) {
		return f.c.scanOff, max(f.c.scanBlock, firstLen)
	}
	f.c.scanFrames = 0
	return frameEnd, firstLen
}

// withinBlockBound refuses a block past the bytes a Huffman-coded field list
// could take, or spread over too many frames: a CONTINUATION flood. The
// connection ends with ENHANCE_YOUR_CALM before anything is copied.
func (f *feedRun) withinBlockBound(blockLen int32) bool {
	//: the block's bytes are charged against the header-list ceiling, the frames counted.
	if int(blockLen) > f.l.limits.MaxHeaderList*blockBoundFactor || f.c.scanFrames >= maxContinuationFrames {
		f.connError(corehttp2.ErrCodeEnhanceYourCalm)
		return false
	}
	return true
}

// nextContinuation reads the next frame of a field block: it must be a
// CONTINUATION of the block's stream, §6.10, and whole.
func (f *feedRun) nextContinuation(in []byte) (corehttp2.FrameValue, int, bool) {
	h, n, code := ParseFrameHeader(in, f.l.limits.MaxFrame)
	//: too few bytes for the header.
	if n == 0 {
		return h, 0, false
	}
	//: a refused header ends the connection.
	if code != corehttp2.ErrCodeNoError {
		f.connError(code)
		return h, 0, false
	}
	//: not whole yet.
	if len(in) < n+h.Length {
		return h, 0, false
	}
	//: anything but this stream's CONTINUATION breaks the block, §6.10.
	if h.Type != corehttp2.FrameContinuation || h.StreamID != f.h.StreamID {
		f.connError(corehttp2.ErrCodeProtocol)
		return h, 0, false
	}
	return h, n + h.Length, true
}

// joinSequence builds the whole block of a sequence past one frame.
func (f *feedRun) joinSequence(in, first []byte, end int, endStream bool) headerSequence {
	start := len(f.l.join)
	f.l.join = append(f.l.join, first...)
	off := f.frameLen()
	//: every CONTINUATION of the sequence; the walk has validated them.
	for off < end {
		h, n, _ := ParseFrameHeader(in[off:], f.l.limits.MaxFrame)
		f.l.join = append(f.l.join, in[off+n:off+n+h.Length]...)
		off += n + h.Length
	}
	block := f.l.join[start:len(f.l.join):len(f.l.join)]
	return headerSequence{total: end, block: block, endStream: endStream}
}

// fragmentOf returns a HEADERS payload's field-block fragment, without its pad
// length, padding and priority fields, RFC 9113 §6.2.
func (f *feedRun) fragmentOf(h corehttp2.FrameValue, payload []byte) []byte {
	//: the frame layer has judged the padding.
	if h.Flags&corehttp2.FlagPadded != 0 {
		pad := int(payload[0])
		payload = payload[1 : len(payload)-pad]
	}
	//: PRIORITY fields come first, then the fragment.
	if h.Flags&corehttp2.FlagPriority != 0 {
		//: priority fields past the payload: the frame is impossible.
		if len(payload) < priorityFieldsLen {
			f.connError(corehttp2.ErrCodeFrameSize)
			return nil
		}
		payload = payload[priorityFieldsLen:]
	}
	return payload
}

// onHeaderBlock admits or refuses a complete field block.
func (f *feedRun) onHeaderBlock(seq *headerSequence) {
	idx, _ := f.l.find(f.c, f.h.StreamID)
	//: a HEADERS on an open stream is a trailer or a closed stream's frame.
	if idx != noIndex {
		f.headersOnOpenStream(idx, seq)
		return
	}
	//: an id at or below the last opened cannot open a stream, §5.1.1.
	if f.h.StreamID <= f.c.lastStream {
		f.connError(corehttp2.ErrCodeProtocol)
		return
	}
	f.c.lastStream = f.h.StreamID
	//: a stream past the limit, or after our GOAWAY, is refused but still decoded.
	if f.c.has(flagGoAway) || int(f.c.open) >= f.l.limits.MaxStreams {
		f.emit(Event{Kind: EventHeaders, Stream: f.h.StreamID, Data: seq.block, Flags: EventDiscard})
		f.l.countErr(corehttp2.ErrCodeRefusedStream)
		f.dst = AppendRSTStream(f.dst, f.h.StreamID, corehttp2.ErrCodeRefusedStream)
		return
	}
	f.c.lastOpen = f.h.StreamID
	idx = f.l.newStream(f.c, f.h.StreamID)
	//: a request with no body closes the peer's half at once.
	if seq.endStream {
		f.l.streams[idx].set(streamRemoteClosed)
	}
	f.emit(Event{Kind: EventHeaders, Stream: f.h.StreamID, Data: seq.block, Flags: endFlag(seq.endStream)})
}

// headersOnOpenStream refuses a HEADERS frame on an open stream: a trailer is
// out of the profile, §8.1's trailers refused as http1 refuses them; a stream
// that already closed its half is STREAM_CLOSED, §5.1. Either way the block is
// decoded for the table's sake.
func (f *feedRun) headersOnOpenStream(idx int32, seq *headerSequence) {
	st := &f.l.streams[idx]
	f.emit(Event{Kind: EventHeaders, Stream: st.id, Data: seq.block, Flags: EventDiscard})
	//: the peer's half already ended: the stream is closed to it.
	if st.has(streamRemoteClosed) {
		f.streamError(idx, corehttp2.ErrCodeStreamClosed)
		return
	}
	f.streamError(idx, corehttp2.ErrCodeProtocol)
}

// continuationFrame refuses a CONTINUATION outside a field block, §6.10: a
// sequence is read whole by headersFrame, so one seen here is out of place.
func (f *feedRun) continuationFrame() int {
	f.connError(corehttp2.ErrCodeProtocol)
	return 0
}

// endFlag builds an event's END_STREAM flag.
func endFlag(end bool) EventFlags {
	//: the frame carried END_STREAM.
	if end {
		return EventEndStream
	}
	return 0
}
