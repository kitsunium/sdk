package http2

import (
	"encoding/binary"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The frame layer's wire numbers, RFC 9113 §4.1 and §6.
const (
	// streamIDMask clears the reserved bit of a frame header's stream field.
	streamIDMask uint32 = 1<<31 - 1
	// paddedHeaderLen is the bytes a padded frame's pad length needs read.
	paddedHeaderLen int = 10
	// settingLen is the wire length of one SETTINGS parameter.
	settingLen int = 6
	// windowUpdateLen is the payload of a WINDOW_UPDATE frame.
	windowUpdateLen int = 4
	// rstStreamLen is the payload of an RST_STREAM frame.
	rstStreamLen int = 4
	// goAwayLen is the payload of a GOAWAY frame with no debug data.
	goAwayLen int = 8
	// pingLen is the payload of a PING frame.
	pingLen int = 8
	// byte0Shift and byte1Shift and byte2Shift place the frame length's bytes.
	byte0Shift uint = 16
	byte1Shift uint = 8
)

// ParseFrameHeader reads a frame's nine-byte header from buf, refusing what those
// bytes alone can refuse: a length past maxFrame (FRAME_SIZE_ERROR) and a padded
// frame whose padding is not shorter than its payload (PROTOCOL_ERROR). It returns the
// header and the 9 bytes it consumed, or 0 while buf holds too few of them — a
// padded frame's pad length is read here too, so it needs ten. A frame judged
// whole by this call is not yet legal on any stream: that is the connection state
// machine's. It allocates nothing.
func ParseFrameHeader(buf []byte, maxFrame int) (corehttp2.FrameValue, int, corehttp2.ErrCode) {
	//: too few bytes: the caller reads more.
	if len(buf) < corehttp2.FrameHeaderLen {
		return corehttp2.FrameValue{}, 0, corehttp2.ErrCodeNoError
	}
	h := corehttp2.FrameValue{
		Type:     corehttp2.FrameType(buf[3]),
		Flags:    corehttp2.FrameFlags(buf[4]),
		StreamID: binary.BigEndian.Uint32(buf[5:9]) & streamIDMask,
		Length:   int(buf[0])<<byte0Shift | int(buf[1])<<byte1Shift | int(buf[2]),
	}
	//: the frame bound is judged on the length alone, before a payload byte arrives.
	if h.Length > maxFrame {
		return h, corehttp2.FrameHeaderLen, corehttp2.ErrCodeFrameSize
	}
	//: only DATA and HEADERS carry a pad length.
	if !isPaddedFrame(h) {
		return h, corehttp2.FrameHeaderLen, corehttp2.ErrCodeNoError
	}
	return judgePadding(buf, h)
}

// isPaddedFrame reports whether h is a frame that carries a pad length.
func isPaddedFrame(h corehttp2.FrameValue) bool {
	return h.Flags&corehttp2.FlagPadded != 0 &&
		(h.Type == corehttp2.FrameData || h.Type == corehttp2.FrameHeaders)
}

// judgePadding reads a padded frame's pad length and refuses padding that does
// not leave the rest of the payload shorter than the frame's length.
func judgePadding(buf []byte, h corehttp2.FrameValue) (corehttp2.FrameValue, int, corehttp2.ErrCode) {
	//: a padded frame with no payload has no room for its pad length.
	if h.Length == 0 {
		return h, corehttp2.FrameHeaderLen, corehttp2.ErrCodeProtocol
	}
	//: the pad length is the first payload byte: wait for it.
	if len(buf) < paddedHeaderLen {
		return corehttp2.FrameValue{}, 0, corehttp2.ErrCodeNoError
	}
	//: RFC 9113 §6.1: padding the length of the payload or more is a protocol error.
	if int(buf[corehttp2.FrameHeaderLen]) >= h.Length {
		return h, corehttp2.FrameHeaderLen, corehttp2.ErrCodeProtocol
	}
	return h, corehttp2.FrameHeaderLen, corehttp2.ErrCodeNoError
}

// AppendFrameHeader appends h's nine bytes to dst.
func AppendFrameHeader(dst []byte, h corehttp2.FrameValue) []byte {
	return append(dst,
		byte(h.Length>>byte0Shift), byte(h.Length>>byte1Shift), byte(h.Length),
		byte(h.Type), byte(h.Flags),
		byte(h.StreamID>>24), byte(h.StreamID>>16), byte(h.StreamID>>8), byte(h.StreamID))
}

// AppendSettings appends a SETTINGS frame carrying settings, in order.
func AppendSettings(dst []byte, settings []corehttp2.SettingValue) []byte {
	dst = AppendFrameHeader(dst, corehttp2.FrameValue{
		Type:   corehttp2.FrameSettings,
		Length: len(settings) * settingLen,
	})
	//: each parameter is six bytes: a 16-bit identifier and a 32-bit value.
	for _, s := range settings {
		dst = binary.BigEndian.AppendUint16(dst, uint16(s.ID))
		dst = binary.BigEndian.AppendUint32(dst, s.Val)
	}
	return dst
}

// AppendSettingsAck appends the empty SETTINGS frame that acknowledges the peer's.
func AppendSettingsAck(dst []byte) []byte {
	return AppendFrameHeader(dst, corehttp2.FrameValue{Type: corehttp2.FrameSettings, Flags: corehttp2.FlagAck})
}

// AppendWindowUpdate appends a WINDOW_UPDATE granting increment on stream, zero for the connection.
func AppendWindowUpdate(dst []byte, stream, increment uint32) []byte {
	dst = AppendFrameHeader(dst, corehttp2.FrameValue{
		Type: corehttp2.FrameWindowUpdate, StreamID: stream, Length: windowUpdateLen,
	})
	return binary.BigEndian.AppendUint32(dst, increment&streamIDMask)
}

// AppendRSTStream appends an RST_STREAM ending stream with code.
func AppendRSTStream(dst []byte, stream uint32, code corehttp2.ErrCode) []byte {
	dst = AppendFrameHeader(dst, corehttp2.FrameValue{
		Type: corehttp2.FrameRSTStream, StreamID: stream, Length: rstStreamLen,
	})
	return binary.BigEndian.AppendUint32(dst, uint32(code))
}

// AppendGoAway appends a GOAWAY naming the last stream the sender processed and code.
func AppendGoAway(dst []byte, last uint32, code corehttp2.ErrCode) []byte {
	dst = AppendFrameHeader(dst, corehttp2.FrameValue{Type: corehttp2.FrameGoAway, Length: goAwayLen})
	dst = binary.BigEndian.AppendUint32(dst, last&streamIDMask)
	return binary.BigEndian.AppendUint32(dst, uint32(code))
}

// AppendPing appends a PING frame carrying data, the ACK flag set when ack.
func AppendPing(dst []byte, ack bool, data *[8]byte) []byte {
	h := corehttp2.FrameValue{Type: corehttp2.FramePing, Length: pingLen}
	//: the flag is the only difference between a PING and its answer.
	if ack {
		h.Flags = corehttp2.FlagAck
	}
	dst = AppendFrameHeader(dst, h)
	return append(dst, data[:]...)
}
