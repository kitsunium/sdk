package http2

import (
	"encoding/binary"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The wire layouts of the frames the state machine reads, RFC 9113 §6.
const (
	// priorityFieldsLen is a HEADERS frame's priority field length.
	priorityFieldsLen int = 5
	// priorityFrameLen is a PRIORITY frame's payload length.
	priorityFrameLen int = 5
	// streamIDFieldLen is the bytes of a stream id or an error code.
	streamIDFieldLen int = 4
	// goAwayMinLen is a GOAWAY's fixed payload.
	goAwayMinLen int = 8
	// pingPayloadLen is a PING's fixed payload.
	pingPayloadLen int = 8
)

// priorityFrame ignores the deprecated priority signal, §6.3, once it is well
// formed: it opens no stream and takes no work.
func (f *feedRun) priorityFrame() int {
	//: PRIORITY names a stream, §6.3.
	if f.h.StreamID == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: a malformed PRIORITY frame is a stream error, §6.3.
	if f.h.Length != priorityFrameLen {
		f.l.countErr(corehttp2.ErrCodeFrameSize)
		f.dst = AppendRSTStream(f.dst, f.h.StreamID, corehttp2.ErrCodeFrameSize)
	}
	f.spendControl()
	return f.frameLen()
}

// rstFrame handles RST_STREAM, §6.4.
func (f *feedRun) rstFrame() int {
	//: RST_STREAM belongs to a stream, §6.4.
	if f.h.StreamID == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: the payload is one error code, §6.4.
	if f.h.Length != streamIDFieldLen {
		f.connError(corehttp2.ErrCodeFrameSize)
		return 0
	}
	idx, _ := f.l.find(f.c, f.h.StreamID)
	//: a reset of a stream never opened breaks the connection; a closed one is ignored.
	if idx == noIndex {
		f.rstOnAbsent()
		return f.frameLen()
	}
	code := corehttp2.ErrCode(binary.BigEndian.Uint32(f.payload))
	f.l.closeStream(f.c, idx)
	f.emit(Event{Kind: EventReset, Stream: f.h.StreamID, Code: code})
	f.spendReset()
	return f.frameLen()
}

// rstOnAbsent answers a RST_STREAM for a stream not on the list.
func (f *feedRun) rstOnAbsent() {
	//: a stream that was never opened cannot be reset, §6.4.
	if f.h.StreamID > f.c.lastStream {
		f.connError(corehttp2.ErrCodeProtocol)
	}
	f.spendControl()
}

// spendReset charges one reset to the Rapid Reset budget, CVE-2023-44487: a
// client that opens and cancels streams without letting any finish pays for
// each, and ends the connection when the budget is gone.
func (f *feedRun) spendReset() {
	f.c.resets--
	//: the budget's gone: this peer only cancels.
	if f.c.resets <= 0 {
		f.connError(corehttp2.ErrCodeEnhanceYourCalm)
	}
}

// pushFrame refuses a PUSH_PROMISE: a client cannot push, §8.4.
func (f *feedRun) pushFrame() int {
	f.connError(corehttp2.ErrCodeProtocol)
	return 0
}

// pingFrame answers a PING, §6.7.
func (f *feedRun) pingFrame() int {
	//: PING belongs to the connection, §6.7.
	if f.h.StreamID != 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: a PING's payload is eight bytes, §6.7.
	if f.h.Length != pingPayloadLen {
		f.connError(corehttp2.ErrCodeFrameSize)
		return 0
	}
	f.spendControl()
	//: an ACK needs no answer; the server sends no PING of its own.
	if f.h.Flags&corehttp2.FlagAck == 0 && !f.c.has(flagClosing) {
		var data [pingPayloadLen]byte
		copy(data[:], f.payload)
		f.dst = AppendPing(f.dst, true, &data)
	}
	return f.frameLen()
}

// goAwayFrame reads the peer's GOAWAY, §6.8: it opens nothing new.
func (f *feedRun) goAwayFrame() int {
	//: GOAWAY belongs to the connection, §6.8.
	if f.h.StreamID != 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: at least the last stream and the error code, §6.8.
	if f.h.Length < goAwayMinLen {
		f.connError(corehttp2.ErrCodeFrameSize)
		return 0
	}
	f.c.set(flagPeerGoAway)
	code := corehttp2.ErrCode(binary.BigEndian.Uint32(f.payload[streamIDFieldLen:]))
	f.emit(Event{Kind: EventGoAway, Code: code})
	return f.frameLen()
}

// windowFrame handles WINDOW_UPDATE, §6.9.
func (f *feedRun) windowFrame() int {
	//: the payload is one 31-bit increment, §6.9.
	if f.h.Length != streamIDFieldLen {
		f.connError(corehttp2.ErrCodeFrameSize)
		return 0
	}
	f.spendControl()
	inc := binary.BigEndian.Uint32(f.payload) & streamIDMask
	//: the connection's window, or a stream's.
	if f.h.StreamID == 0 {
		f.connectionCredit(inc)
	} else {
		f.streamCredit(inc)
	}
	return f.frameLen()
}

// connectionCredit applies a WINDOW_UPDATE on the connection, §6.9.1.
func (f *feedRun) connectionCredit(inc uint32) {
	//: a zero increment on the connection is a connection error, §6.9.
	if inc == 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return
	}
	next := int64(f.c.sendWindow) + int64(inc)
	//: past the protocol's maximum, §6.9.1.
	if next > int64(corehttp2.MaxWindow) {
		f.connError(corehttp2.ErrCodeFlowControl)
		return
	}
	f.c.sendWindow = int32(next)
	f.l.flushHolds(f)
}

// streamCredit applies a WINDOW_UPDATE on a stream, §6.9.1.
func (f *feedRun) streamCredit(inc uint32) {
	idx, _ := f.l.find(f.c, f.h.StreamID)
	//: on a closed stream it may race and is ignored; on an idle one it is an error.
	if idx == noIndex {
		f.creditOnAbsent(inc)
		return
	}
	st := &f.l.streams[idx]
	//: a zero increment on a stream is a stream error, §6.9.
	if inc == 0 {
		f.streamError(idx, corehttp2.ErrCodeProtocol)
		return
	}
	next := int64(st.sendWindow) + int64(inc)
	//: past the protocol's maximum, §6.9.1.
	if next > int64(corehttp2.MaxWindow) {
		f.streamError(idx, corehttp2.ErrCodeFlowControl)
		return
	}
	st.sendWindow = int32(next)
	f.l.flushHolds(f)
}

// creditOnAbsent answers a WINDOW_UPDATE for a stream not on the list.
func (f *feedRun) creditOnAbsent(inc uint32) {
	//: a stream never opened cannot be credited, §5.1.
	if f.h.StreamID > f.c.lastStream {
		f.connError(corehttp2.ErrCodeProtocol)
		return
	}
	//: a zero increment is still malformed, even on a closed stream.
	if inc == 0 {
		f.l.countErr(corehttp2.ErrCodeProtocol)
		f.dst = AppendRSTStream(f.dst, f.h.StreamID, corehttp2.ErrCodeProtocol)
	}
}
