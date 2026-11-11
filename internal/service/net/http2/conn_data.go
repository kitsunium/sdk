package http2

import (
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// dataFrame handles a DATA frame, RFC 9113 §6.1.
func (f *feedRun) dataFrame() int {
	//: DATA belongs to a stream, §6.1.
	if f.h.StreamID == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	idx, _ := f.l.find(f.c, f.h.StreamID)
	//: not an open stream: idle is a connection error, closed a stream error.
	if idx == noIndex {
		f.dataOnAbsent()
		return f.frameLen()
	}
	f.dataOnStream(idx)
	return f.frameLen()
}

// dataOnAbsent answers DATA for a stream not on the list: idle is a connection
// error, §5.1; a closed stream is a stream error, and its bytes still count
// against the connection, §5.2.
func (f *feedRun) dataOnAbsent() {
	//: an id above every opened one has never been opened.
	if f.h.StreamID > f.c.lastStream {
		f.connError(corehttp2.ErrCodeProtocol)
		return
	}
	f.spendControl()
	f.discardAgainstConn()
	//: the charge may have ended the connection; its GOAWAY is the last frame.
	if !f.c.has(flagClosing) {
		f.l.countErr(corehttp2.ErrCodeStreamClosed)
		f.dst = AppendRSTStream(f.dst, f.h.StreamID, corehttp2.ErrCodeStreamClosed)
	}
}

// discardAgainstConn charges a frame to the connection window and credits it
// back at once: no handler will read these bytes.
func (f *feedRun) discardAgainstConn() {
	//: flow control counts them, §5.2, whatever the stream is.
	if int(f.c.peerWindow) < f.h.Length {
		f.connError(corehttp2.ErrCodeFlowControl)
		return
	}
	f.c.peerWindow -= int32(f.h.Length)
	f.c.peerCredit += int32(f.h.Length)
}

// dataOnStream receives DATA on an open or half-closed stream.
func (f *feedRun) dataOnStream(idx int32) {
	st := &f.l.streams[idx]
	//: §5.1: after END_STREAM only WINDOW_UPDATE, PRIORITY and RST_STREAM may come.
	if st.has(streamRemoteClosed) {
		f.discardAgainstConn()
		f.streamError(idx, corehttp2.ErrCodeStreamClosed)
		return
	}
	//: both windows count the whole payload, padding and pad length included.
	if int(f.c.peerWindow) < f.h.Length {
		f.connError(corehttp2.ErrCodeFlowControl)
		return
	}
	//: a stream past its window is a stream error, §6.9.1.
	if int(st.peerWindow) < f.h.Length {
		f.discardAgainstConn()
		f.streamError(idx, corehttp2.ErrCodeFlowControl)
		return
	}
	f.c.peerWindow -= int32(f.h.Length)
	st.peerWindow -= int32(f.h.Length)
	f.deliverData(idx)
}

// deliverData strips the padding, accumulates the body and reports the
// event; the padding is credited back at once since nothing reads it.
func (f *feedRun) deliverData(idx int32) {
	st := &f.l.streams[idx]
	payload := f.payload
	pad := 0
	//: a padded frame's first byte is its pad length; the frame layer checked it.
	if f.h.Flags&corehttp2.FlagPadded != 0 {
		pad = int(payload[0])
		payload = payload[1 : len(payload)-pad]
	}
	//: pad length byte and padding are nobody's to read.
	credit := int32(f.h.Length - len(payload))
	st.peerCredit += credit
	f.c.peerCredit += credit
	end := f.h.Flags&corehttp2.FlagEndStream != 0
	over := st.has(streamOverBody) || int64(st.bodyLen)+int64(len(payload)) > int64(f.l.maxBody)
	//: a body past the bound is not kept: its bytes are nobody's to read either,
	//: and the adapter refuses the request once (413) when it sees the flag.
	if over {
		st.set(streamOverBody)
		st.peerCredit += int32(len(payload))
		f.c.peerCredit += int32(len(payload))
	}
	st.bodyLen += int32(len(payload))
	//: the peer finished its half.
	if end {
		st.set(streamRemoteClosed)
	}
	//: an empty frame without END_STREAM is a frame that does nothing.
	if len(payload) == 0 && !end {
		f.spendControl()
	}
	f.emit(Event{Kind: EventData, Stream: st.id, Data: payload, Flags: dataFlags(end, over)})
	//: the response may have ended first; the stream closes when both halves are done.
	if end {
		f.finishIfDone(idx)
	}
}

// dataFlags builds an EventData's flags.
func dataFlags(end, over bool) EventFlags {
	flags := endFlag(end)
	//: past the body bound, the adapter refuses the request once.
	if over {
		flags |= EventOverBody
	}
	return flags
}
