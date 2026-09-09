// Package net — the WebSocket frame header and its wire form (RFC 6455 §5).
package net

import (
	"encoding/binary"
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// WSMaskLen is the width of the masking key a client prefixes to every frame.
const WSMaskLen int = 4

// WSMaxControlPayload is the ceiling RFC 6455 §5.5 puts on a control frame's
// payload. It exists so an endpoint can answer a Ping without buffering: a
// control frame is answered inline, between the fragments of a message, so it
// must fit in a fixed buffer that is always available.
const WSMaxControlPayload int = 125

// WSMinHeaderLen is the shortest possible frame header: flags/opcode and the
// mask bit with a 7-bit length.
const WSMinHeaderLen int = 2

// WSMaxHeaderLen is the longest possible frame header: the two fixed bytes, a
// 64-bit extended length, and a masking key.
const WSMaxHeaderLen int = WSMinHeaderLen + wsLength64Width + WSMaskLen

// Frame header bit masks and length markers, from the diagram in RFC 6455 §5.2.
const (
	wsFinBit         byte = 0x80
	wsReservedBits   byte = 0x70
	wsOpCodeBits     byte = 0x0F
	wsMaskBit        byte = 0x80
	wsLengthBits     byte = 0x7F
	wsLength16Marker byte = 126
	wsLength64Marker byte = 127
	wsLength16Width  int  = 2
	wsLength64Width  int  = 8
)

// WSFrameHeaderValue is one parsed frame header (RFC 6455 §5.2).
//
// The RSV bits are absent on purpose. They only mean anything once an extension
// has been negotiated, this domain negotiates none, and a set RSV bit is
// therefore a protocol error rather than a value to carry — see
// ParseWSFrameHeader.
type WSFrameHeaderValue struct {
	// Final is the FIN bit: this frame completes the message.
	Final bool `json:"final"`
	// OpCode is the frame type.
	OpCode WSOpCode `json:"opcode"`
	// Masked is the MASK bit. RFC 6455 §5.1 requires it on every client frame
	// and forbids it on every server frame.
	Masked bool `json:"masked"`
	// MaskKey is the four-byte key, meaningful only when Masked.
	MaskKey [WSMaskLen]byte `json:"mask_key"`
	// Length is the payload length the frame announces. It is what the peer
	// SAYS, not what it has sent — every bound must be checked against it
	// BEFORE any buffer is sized from it.
	Length uint64 `json:"length"`
}

// ValidateFromClient enforces the one framing rule whose answer depends on the
// direction of travel: RFC 6455 §5.1 requires every client-to-server frame to
// be masked.
//
// An unmasked client frame FAILS THE CONNECTION. That is not pedantry about a
// key that protects nothing cryptographically: masking exists so a hostile
// script cannot steer a browser into emitting bytes that a transparent
// intermediary would read as a second, attacker-chosen HTTP request. An
// endpoint that accepted unmasked frames would let that script skip the one
// mechanism the design has against cache poisoning.
func (h WSFrameHeaderValue) ValidateFromClient() error {
	//: the requirement is absolute; there is no tolerant reading of it.
	if !h.Masked {
		//: fail the connection, per §5.1.
		return errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", h.OpCode.String()),
			errs.String("why", "a client-to-server frame must be masked"))
	}
	//: masked as required.
	return nil
}

// WSFrameHeaderLen returns the full header length announced by the first two
// bytes of a frame, or 0 when fewer than two bytes are available.
//
// It exists so a reader can take exactly two bytes, learn how many more the
// header needs, and take exactly those — never speculating past the header into
// a payload it has not yet bounded.
func WSFrameHeaderLen(b []byte) int {
	//: the two fixed bytes are what announces everything else.
	if len(b) < WSMinHeaderLen {
		//: not enough to know anything yet.
		return 0
	}
	length := WSMinHeaderLen
	//: the 7-bit length field is a marker when it reaches 126.
	switch b[1] & wsLengthBits {
	//: a 16-bit length follows.
	case wsLength16Marker:
		length += wsLength16Width
	//: a 64-bit length follows.
	case wsLength64Marker:
		length += wsLength64Width
	}
	//: a masked frame carries its key immediately after the length.
	if b[1]&wsMaskBit != 0 {
		length += WSMaskLen
	}
	//: the exact number of bytes the header occupies.
	return length
}

// ParseWSFrameHeader parses a COMPLETE frame header and enforces every rule
// RFC 6455 states about it that does not depend on which side sent it.
//
// The masking requirement is deliberately NOT here: it is the one rule whose
// answer depends on the direction of travel, so it lives in
// [WSFrameHeaderValue.ValidateFromClient] where the direction is named. Every
// other rule — reserved bits, reserved opcodes, control-frame shape, minimal
// length encoding — is absolute and is checked here, once.
func ParseWSFrameHeader(b []byte) (header WSFrameHeaderValue, err error) {
	want := WSFrameHeaderLen(b)
	//: the caller is expected to have read exactly WSFrameHeaderLen bytes; a
	//: short or long slice is a bug here, not a peer's fault.
	if want == 0 || len(b) != want {
		//: refuse rather than parse a header that is not all present.
		return WSFrameHeaderValue{}, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("have", len(b)),
			errs.Int("want", want),
			errs.String("why", "the frame header is not complete"))
	}
	//: a reserved bit only means something under a negotiated extension. This
	//: domain negotiates none — permessage-deflate included — so a set bit is
	//: a peer compressing into a reader that would hand the application
	//: compressed bytes as if they were the message.
	if b[0]&wsReservedBits != 0 {
		//: fail the connection rather than deliver a payload we cannot decode.
		return WSFrameHeaderValue{}, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("rsv", int(b[0]&wsReservedBits)),
			errs.String("why", "a reserved bit is set but no extension was negotiated"))
	}
	parsed := WSFrameHeaderValue{
		Final:  b[0]&wsFinBit != 0,
		OpCode: WSOpCode(b[0] & wsOpCodeBits),
		Masked: b[1]&wsMaskBit != 0,
	}
	//: a reserved opcode cannot be skipped: the peer believes it said
	//: something, and an endpoint that ignored it would be desynchronised.
	if !parsed.OpCode.Defined() {
		//: fail the connection.
		return WSFrameHeaderValue{}, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("opcode", int(parsed.OpCode)),
			errs.String("why", "the opcode is reserved"))
	}
	length, lerr := parseWSLength(b)
	//: a length the encoding cannot honestly express.
	if lerr != nil {
		//: the error already names what was wrong with it.
		return WSFrameHeaderValue{}, lerr
	}
	parsed.Length = length
	//: control frames must be answerable inline, between two fragments of a
	//: message, which is only possible if they are short and whole.
	if cerr := validateWSControlShape(parsed); cerr != nil {
		//: the error already names which half was violated.
		return WSFrameHeaderValue{}, cerr
	}
	//: the mask key sits at the very end of the header.
	if parsed.Masked {
		copy(parsed.MaskKey[:], b[want-WSMaskLen:want])
	}
	//: a header that satisfies every direction-independent rule.
	return parsed, nil
}

// ApplyWSMask XORs payload in place with the frame's masking key.
//
// The transform is its own inverse, so the same call unmasks what it masked.
// It is done in place because the payload has already been read into the
// buffer that will be handed to the application: copying it out to unmask would
// double the cost of every frame for no gain.
func ApplyWSMask(payload []byte, key [WSMaskLen]byte) {
	//: the key repeats every four bytes from the START of the payload, so the
	//: index into it is the index into the payload modulo four.
	for i := range payload {
		payload[i] ^= key[i&(WSMaskLen-1)]
	}
}

// AppendWSFrame appends one server frame to dst and returns the extended slice.
//
// The frame is never masked, because RFC 6455 §5.1 forbids a server from
// masking and a client that receives a masked frame fails the connection. That
// is expressed by the absence of a parameter rather than by a documented
// default: there is no argument a caller could pass to get it wrong.
//
// Appending rather than allocating is what lets a connection reuse one buffer
// for its whole life.
func AppendWSFrame(dst []byte, op WSOpCode, final bool, payload []byte) (wire []byte, err error) {
	//: validate everything before touching dst, so a refusal leaves the
	//: caller's buffer exactly as it was and a stream never carries half a
	//: frame the peer has already begun parsing.
	if verr := validateWSOutbound(op, final, len(payload)); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	first := byte(op)
	//: the FIN bit says this frame completes its message.
	if final {
		first |= wsFinBit
	}
	dst = append(dst, first)
	//: the length is written in the shortest form that can carry it — the
	//: minimal-encoding rule this package enforces on the way in.
	switch size := len(payload); {
	//: the 7-bit field carries it outright.
	case size < int(wsLength16Marker):
		dst = append(dst, byte(size))
	//: the 16-bit extension.
	case size <= math.MaxUint16:
		dst = append(dst, wsLength16Marker)
		dst = binary.BigEndian.AppendUint16(dst, uint16(size))
	//: the 64-bit extension.
	default:
		dst = append(dst, wsLength64Marker)
		dst = binary.BigEndian.AppendUint64(dst, uint64(size))
	}
	//: no mask key: the server never masks.
	return append(dst, payload...), nil
}

// parseWSLength reads the payload length, enforcing the minimal-encoding rule.
func parseWSLength(b []byte) (length uint64, err error) {
	marker := b[1] & wsLengthBits
	//: a 7-bit length under the markers is the length itself.
	if marker < wsLength16Marker {
		//: the common case: everything up to 125 bytes.
		return uint64(marker), nil
	}
	//: the 16-bit form.
	if marker == wsLength16Marker {
		//: parsed and checked against the form below it.
		return parseWSLength16(b)
	}
	//: the 64-bit form.
	return parseWSLength64(b)
}

// parseWSLength16 reads the 16-bit extended length.
func parseWSLength16(b []byte) (length uint64, err error) {
	value := uint64(binary.BigEndian.Uint16(b[WSMinHeaderLen:]))
	//: RFC 6455 §5.2 requires the MINIMAL encoding. A short length spelled
	//: long is not a harmless alternative: it is a second spelling of the same
	//: frame, which is exactly the ambiguity a length-prefixed protocol cannot
	//: afford between two parsers that disagree.
	if value < uint64(wsLength16Marker) {
		//: refuse the non-minimal encoding.
		return 0, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int64("length", int64(value)),
			errs.String("why", "a 16-bit length below 126 is not the minimal encoding"))
	}
	//: a legitimately 16-bit length.
	return value, nil
}

// parseWSLength64 reads the 64-bit extended length.
func parseWSLength64(b []byte) (length uint64, err error) {
	value := binary.BigEndian.Uint64(b[WSMinHeaderLen:])
	//: the RFC reserves the most significant bit, so a length with it set is
	//: not a very large frame — it is a malformed one.
	if value > math.MaxInt64 {
		//: refuse rather than treat the sign bit as magnitude.
		return 0, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.String("why", "the 64-bit length has its most significant bit set"))
	}
	//: the same minimal-encoding rule, one step up.
	if value <= math.MaxUint16 {
		//: refuse the non-minimal encoding.
		return 0, errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int64("length", int64(value)),
			errs.String("why", "a 64-bit length that fits in 16 bits is not the minimal encoding"))
	}
	//: a legitimately 64-bit length.
	return value, nil
}

// validateWSControlShape enforces RFC 6455 §5.5 on a control frame.
func validateWSControlShape(header WSFrameHeaderValue) error {
	//: a data frame has neither restriction.
	if !header.OpCode.IsControl() {
		//: nothing to check.
		return nil
	}
	//: a fragmented control frame could not be answered until its last
	//: fragment arrived, which a peer is free never to send — so the rule that
	//: makes Ping answerable at all is that a control frame is always whole.
	if !header.Final {
		//: fail the connection.
		return errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", header.OpCode.String()),
			errs.String("why", "a control frame must not be fragmented"))
	}
	//: 125 bytes is what lets a control frame be answered from a fixed buffer
	//: that is always available, even mid-message.
	if header.Length > uint64(WSMaxControlPayload) {
		//: fail the connection.
		return errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", header.OpCode.String()),
			errs.Int64("length", int64(header.Length)),
			errs.String("why", "a control frame payload must not exceed 125 bytes"))
	}
	//: a shape a peer can always answer.
	return nil
}

// validateWSOutbound refuses a frame this endpoint must not put on the wire.
func validateWSOutbound(op WSOpCode, final bool, size int) error {
	//: a reserved opcode is unusable in both directions.
	if !op.Defined() {
		//: refuse before anything is written.
		return errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("opcode", int(op)),
			errs.String("why", "the opcode is reserved"))
	}
	//: emitting what we would refuse to receive is how two implementations of
	//: the same RFC end up disagreeing, so the control-frame rules are checked
	//: on the way out too.
	return validateWSControlShape(WSFrameHeaderValue{
		Final:  final,
		OpCode: op,
		Length: uint64(size),
	})
}
