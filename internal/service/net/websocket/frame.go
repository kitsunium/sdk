// Package websocket — the frame header and its wire form (RFC 6455 §5).
//
// Reading and writing frames is this package's mechanism, not the domain's
// contract (ADR 0160 §4): it moved here from internal/core/net, which keeps
// what a second implementation would share — the opcode, the close code, the
// message and the control-frame ceiling (corenet.WSMaxControlPayload).
package websocket

import (
	"encoding/binary"
	"math"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MaskLen is the width of the masking key a client prefixes to every frame.
const MaskLen int = 4

// MinHeaderLen is the shortest possible frame header: flags/opcode and the
// mask bit with a 7-bit length.
const MinHeaderLen int = 2

// MaxHeaderLen is the longest possible frame header: the two fixed bytes, a
// 64-bit extended length, and a masking key.
const MaxHeaderLen int = MinHeaderLen + length64Width + MaskLen

// Frame header bit masks and length markers, from the diagram in RFC 6455 §5.2.
const (
	finBit         byte = 0x80
	reservedBits   byte = 0x70
	opCodeBits     byte = 0x0F
	maskBit        byte = 0x80
	lengthBits     byte = 0x7F
	length16Marker byte = 126
	length64Marker byte = 127
	length16Width  int  = 2
	length64Width  int  = 8
)

// Masking strides. They are not protocol constants — RFC 6455 fixes only
// MaskLen — but sizes ApplyMask moves the payload in, and both are
// multiples of MaskLen so the key never has to be rotated to stay aligned.
const (
	// maskWordWidth is one 64-bit word: the four-byte key spelled twice.
	maskWordWidth int = 2 * MaskLen
	// maskBlockWidth is four of those, unrolled so the loop's compare and
	// branch are amortised over four words instead of one.
	maskBlockWidth int = maskWordsPerBlock * maskWordWidth
	// maskWordsPerBlock is how many words one block iteration writes. It is
	// four because that is where the store port saturates on the machine
	// BENCH.md was taken on; nothing in the protocol prefers any value.
	maskWordsPerBlock int = 4
	// maskQuadBits is the masking key's width in BITS, which is how far the
	// key must be shifted to spell it a second time inside one word.
	maskQuadBits int = 8 * MaskLen
	// The four word offsets inside one block, named so the unrolled writes
	// cannot drift out of step with maskWordWidth.
	maskWordAt0 int = 0 * maskWordWidth
	maskWordAt1 int = 1 * maskWordWidth
	maskWordAt2 int = 2 * maskWordWidth
	maskWordAt3 int = 3 * maskWordWidth
)

// FrameHeaderValue is one parsed frame header (RFC 6455 §5.2).
//
// The RSV bits are absent on purpose. They only mean anything once an extension
// has been negotiated, this domain negotiates none, and a set RSV bit is
// therefore a protocol error rather than a value to carry — see
// ParseFrameHeader.
type FrameHeaderValue struct {
	// Final is the FIN bit: this frame completes the message.
	Final bool `json:"final"`
	// OpCode is the frame type.
	OpCode corenet.WSOpCode `json:"opcode"`
	// Masked is the MASK bit. RFC 6455 §5.1 requires it on every client frame
	// and forbids it on every server frame.
	Masked bool `json:"masked"`
	// MaskKey is the four-byte key, meaningful only when Masked.
	MaskKey [MaskLen]byte `json:"mask_key"`
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
func (h FrameHeaderValue) ValidateFromClient() error {
	//: the requirement is absolute; there is no tolerant reading of it.
	if !h.Masked {
		//: fail the connection, per §5.1.
		return errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", h.OpCode.String()),
			errs.String("why", "a client-to-server frame must be masked"))
	}
	//: masked as required.
	return nil
}

// FrameHeaderLen returns the full header length announced by the first two
// bytes of a frame, or 0 when fewer than two bytes are available.
//
// It exists so a reader can take exactly two bytes, learn how many more the
// header needs, and take exactly those — never speculating past the header into
// a payload it has not yet bounded.
func FrameHeaderLen(b []byte) int {
	//: the two fixed bytes are what announces everything else.
	if len(b) < MinHeaderLen {
		//: not enough to know anything yet.
		return 0
	}
	length := MinHeaderLen
	//: the 7-bit length field is a marker when it reaches 126.
	switch b[1] & lengthBits {
	//: a 16-bit length follows.
	case length16Marker:
		length += length16Width
	//: a 64-bit length follows.
	case length64Marker:
		length += length64Width
	}
	//: a masked frame carries its key immediately after the length.
	if b[1]&maskBit != 0 {
		length += MaskLen
	}
	//: the exact number of bytes the header occupies.
	return length
}

// ParseFrameHeader parses a COMPLETE frame header and enforces every rule
// RFC 6455 states about it that does not depend on which side sent it.
//
// The masking requirement is deliberately NOT here: it is the one rule whose
// answer depends on the direction of travel, so it lives in
// [FrameHeaderValue.ValidateFromClient] where the direction is named. Every
// other rule — reserved bits, reserved opcodes, control-frame shape, minimal
// length encoding — is absolute and is checked here, once.
func ParseFrameHeader(b []byte) (header FrameHeaderValue, err error) {
	want := FrameHeaderLen(b)
	//: the caller is expected to have read exactly FrameHeaderLen bytes; a
	//: short or long slice is a bug here, not a peer's fault.
	if want == 0 || len(b) != want {
		//: refuse rather than parse a header that is not all present.
		return FrameHeaderValue{}, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("have", len(b)),
			errs.Int("want", want),
			errs.String("why", "the frame header is not complete"))
	}
	//: a reserved bit only means something under a negotiated extension. This
	//: domain negotiates none — permessage-deflate included — so a set bit is
	//: a peer compressing into a reader that would hand the application
	//: compressed bytes as if they were the message.
	if b[0]&reservedBits != 0 {
		//: fail the connection rather than deliver a payload we cannot decode.
		return FrameHeaderValue{}, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("rsv", int(b[0]&reservedBits)),
			errs.String("why", "a reserved bit is set but no extension was negotiated"))
	}
	parsed := FrameHeaderValue{
		Final:  b[0]&finBit != 0,
		OpCode: corenet.WSOpCode(b[0] & opCodeBits),
		Masked: b[1]&maskBit != 0,
	}
	//: a reserved opcode cannot be skipped: the peer believes it said
	//: something, and an endpoint that ignored it would be desynchronised.
	if !parsed.OpCode.Defined() {
		//: fail the connection.
		return FrameHeaderValue{}, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("opcode", int(parsed.OpCode)),
			errs.String("why", "the opcode is reserved"))
	}
	length, lerr := parseLength(b)
	//: a length the encoding cannot honestly express.
	if lerr != nil {
		//: the error already names what was wrong with it.
		return FrameHeaderValue{}, lerr
	}
	parsed.Length = length
	//: control frames must be answerable inline, between two fragments of a
	//: message, which is only possible if they are short and whole.
	if cerr := validateControlShape(parsed); cerr != nil {
		//: the error already names which half was violated.
		return FrameHeaderValue{}, cerr
	}
	//: the mask key sits at the very end of the header.
	if parsed.Masked {
		copy(parsed.MaskKey[:], b[want-MaskLen:want])
	}
	//: a header that satisfies every direction-independent rule.
	return parsed, nil
}

// ApplyMask XORs payload in place with the frame's masking key.
//
// The transform is its own inverse, so the same call unmasks what it masked.
// It is done in place because the payload has already been read into the
// buffer that will be handed to the application: copying it out to unmask would
// double the cost of every frame for no gain.
//
// It runs over EVERY inbound byte and there is no way to opt out — RFC 6455
// §5.1 requires every client-to-server frame to be masked — so it moves a
// machine word at a time rather than a byte at a time. A CPU profile of a 4 KiB
// receive attributed 89 % of the whole path to the byte-at-a-time form; the
// word form is measured at sixteen times its throughput at that size, and the
// compiler renders each word as one memory-destination XOR with no vector
// instruction, no assembly and no unsafe.pointer anywhere.
//
// # Why the byte order cancels
//
// [binary.LittleEndian] is used for BOTH the payload word and the key word, and
// that is the entire endianness argument. A fixed-order decode E is a bijection
// between eight bytes and a uint64 under which XOR is bytewise —
// E(a)^E(b) = E(a XOR b), because each byte occupies its own bit field — so
// decoding, XORing and re-encoding with ONE order reproduces the byte-for-byte
// XOR on every machine, big-endian included. What is unsafe is not the choice
// of order but MIXING two, or reinterpreting the slice as words directly, which
// is native-order and would silently disagree with a little-endian key on a
// big-endian host. Neither appears here, and neither can be added without
// changing the one order this function names.
func ApplyMask(payload []byte, key [MaskLen]byte) {
	//: the key repeats every four bytes from the START of the payload, so a
	//: word is just the key spelled twice — and building it by arithmetic
	//: rather than staging it through a byte array keeps the whole set-up in
	//: registers, which is what makes an empty control frame cost the same as
	//: it did before.
	quad := binary.LittleEndian.Uint32(key[:])
	word := uint64(quad) | uint64(quad)<<maskQuadBits
	index := 0
	//: four words per iteration, because one word per iteration leaves the
	//: store port idle waiting on the loop's own compare-and-branch. Every step
	//: is a multiple of MaskLen, which is what keeps the key aligned with the
	//: payload without ever rotating it.
	for ; index+maskBlockWidth <= len(payload); index += maskBlockWidth {
		block := payload[index : index+maskBlockWidth : index+maskBlockWidth]
		binary.LittleEndian.PutUint64(block[maskWordAt0:], binary.LittleEndian.Uint64(block[maskWordAt0:])^word)
		binary.LittleEndian.PutUint64(block[maskWordAt1:], binary.LittleEndian.Uint64(block[maskWordAt1:])^word)
		binary.LittleEndian.PutUint64(block[maskWordAt2:], binary.LittleEndian.Uint64(block[maskWordAt2:])^word)
		binary.LittleEndian.PutUint64(block[maskWordAt3:], binary.LittleEndian.Uint64(block[maskWordAt3:])^word)
	}
	//: whatever the block loop could not take, one word at a time.
	for ; index+maskWordWidth <= len(payload); index += maskWordWidth {
		binary.LittleEndian.PutUint64(payload[index:], binary.LittleEndian.Uint64(payload[index:])^word)
	}
	//: the last seven bytes at most. The key index is the ABSOLUTE index modulo
	//: four and never a fresh count from zero: it only happens to agree here
	//: because every step above is a multiple of four, and writing it the other
	//: way would make this loop's correctness depend on a fact stated three
	//: loops earlier.
	for ; index < len(payload); index++ {
		payload[index] ^= key[index&(MaskLen-1)]
	}
}

// AppendFrame appends one server frame to dst and returns the extended slice.
//
// The frame is never masked, because RFC 6455 §5.1 forbids a server from
// masking and a client that receives a masked frame fails the connection. That
// is expressed by the absence of a parameter rather than by a documented
// default: there is no argument a caller could pass to get it wrong.
//
// Appending rather than allocating is what lets a connection reuse one buffer
// for its whole life.
func AppendFrame(dst []byte, op corenet.WSOpCode, final bool, payload []byte) (wire []byte, err error) {
	//: validate everything before touching dst, so a refusal leaves the
	//: caller's buffer exactly as it was and a stream never carries half a
	//: frame the peer has already begun parsing.
	if verr := validateOutbound(op, final, len(payload)); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	first := byte(op)
	//: the FIN bit says this frame completes its message.
	if final {
		first |= finBit
	}
	dst = append(dst, first)
	//: the length is written in the shortest form that can carry it — the
	//: minimal-encoding rule this package enforces on the way in.
	switch size := len(payload); {
	//: the 7-bit field carries it outright.
	case size < int(length16Marker):
		dst = append(dst, byte(size))
	//: the 16-bit extension.
	case size <= math.MaxUint16:
		dst = append(dst, length16Marker)
		dst = binary.BigEndian.AppendUint16(dst, uint16(size))
	//: the 64-bit extension.
	default:
		dst = append(dst, length64Marker)
		dst = binary.BigEndian.AppendUint64(dst, uint64(size))
	}
	//: no mask key: the server never masks.
	return append(dst, payload...), nil
}

// parseLength reads the payload length, enforcing the minimal-encoding rule.
func parseLength(b []byte) (length uint64, err error) {
	marker := b[1] & lengthBits
	//: a 7-bit length under the markers is the length itself.
	if marker < length16Marker {
		//: the common case: everything up to 125 bytes.
		return uint64(marker), nil
	}
	//: the 16-bit form.
	if marker == length16Marker {
		//: parsed and checked against the form below it.
		return parseLength16(b)
	}
	//: the 64-bit form.
	return parseLength64(b)
}

// parseLength16 reads the 16-bit extended length.
func parseLength16(b []byte) (length uint64, err error) {
	value := uint64(binary.BigEndian.Uint16(b[MinHeaderLen:]))
	//: RFC 6455 §5.2 requires the MINIMAL encoding. A short length spelled
	//: long is not a harmless alternative: it is a second spelling of the same
	//: frame, which is exactly the ambiguity a length-prefixed protocol cannot
	//: afford between two parsers that disagree.
	if value < uint64(length16Marker) {
		//: refuse the non-minimal encoding.
		return 0, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int64("length", int64(value)),
			errs.String("why", "a 16-bit length below 126 is not the minimal encoding"))
	}
	//: a legitimately 16-bit length.
	return value, nil
}

// parseLength64 reads the 64-bit extended length.
func parseLength64(b []byte) (length uint64, err error) {
	value := binary.BigEndian.Uint64(b[MinHeaderLen:])
	//: the RFC reserves the most significant bit, so a length with it set is
	//: not a very large frame — it is a malformed one.
	if value > math.MaxInt64 {
		//: refuse rather than treat the sign bit as magnitude.
		return 0, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.String("why", "the 64-bit length has its most significant bit set"))
	}
	//: the same minimal-encoding rule, one step up.
	if value <= math.MaxUint16 {
		//: refuse the non-minimal encoding.
		return 0, errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int64("length", int64(value)),
			errs.String("why", "a 64-bit length that fits in 16 bits is not the minimal encoding"))
	}
	//: a legitimately 64-bit length.
	return value, nil
}

// validateControlShape enforces RFC 6455 §5.5 on a control frame.
func validateControlShape(header FrameHeaderValue) error {
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
		return errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", header.OpCode.String()),
			errs.String("why", "a control frame must not be fragmented"))
	}
	//: 125 bytes is what lets a control frame be answered from a fixed buffer
	//: that is always available, even mid-message.
	if header.Length > uint64(corenet.WSMaxControlPayload) {
		//: fail the connection.
		return errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.String("opcode", header.OpCode.String()),
			errs.Int64("length", int64(header.Length)),
			errs.String("why", "a control frame payload must not exceed 125 bytes"))
	}
	//: a shape a peer can always answer.
	return nil
}

// validateOutbound refuses a frame this endpoint must not put on the wire.
func validateOutbound(op corenet.WSOpCode, final bool, size int) error {
	//: a reserved opcode is unusable in both directions.
	if !op.Defined() {
		//: refuse before anything is written.
		return errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("opcode", int(op)),
			errs.String("why", "the opcode is reserved"))
	}
	//: emitting what we would refuse to receive is how two implementations of
	//: the same RFC end up disagreeing, so the control-frame rules are checked
	//: on the way out too.
	return validateControlShape(FrameHeaderValue{
		Final:  final,
		OpCode: op,
		Length: uint64(size),
	})
}
