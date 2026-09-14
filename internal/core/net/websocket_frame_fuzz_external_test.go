// Package net_test — the frame header and the Close payload, fuzzed against an
// independent transcription of RFC 6455 rather than against themselves.
//
// Both functions read a length out of bytes a hostile client chose: seven bits,
// sixteen bits or sixty-four, with the wider forms selected by the narrow one.
// That is the shape every length-prefixed parser gets wrong in the same two
// ways — it reads a field that is not all there, or it accepts a length its own
// field could not have carried — and neither is visible from a table of cases
// an author thought of, because the interesting inputs are the ones nobody
// would write down.
//
// So the fuzz targets below do not assert "it did not panic". A parser that
// refused everything would satisfy that, and so would one that accepted
// everything and returned garbage. They assert a VERDICT against an oracle
// transcribed from the RFC's own text — wsFuzzHeaderOracle and
// wsFuzzCloseOracle — which spells out the bit masks, the markers and the
// close-code registry a second time, from the specification, so that a mask or
// a bound edited in websocket_frame.go does not silently edit the expectation
// with it.
package net_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"testing"
	"unicode/utf8"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The frame header's bit fields and length markers, transcribed from the
// diagram in RFC 6455 §5.2.
//
// They are deliberately NOT the package's own constants — those are unexported,
// and importing them would be worse if they were not: an oracle that reads its
// masks from the code under test cannot notice a mask that moved. These are
// read off the specification's diagram, where the MASK bit is the high bit of
// the second byte and the payload length is the seven below it.
const (
	// wsFuzzFinBit is FIN: this frame completes its message.
	wsFuzzFinBit byte = 0x80
	// wsFuzzRSVBits are RSV1-3, meaningful only under a negotiated extension.
	wsFuzzRSVBits byte = 0x70
	// wsFuzzOpCodeBits is the four-bit opcode.
	wsFuzzOpCodeBits byte = 0x0F
	// wsFuzzControlBit is the bit that splits the opcode space at 8.
	wsFuzzControlBit byte = 0x08
	// wsFuzzMaskBit is MASK: a four-byte key closes the header.
	wsFuzzMaskBit byte = 0x80
	// wsFuzzLengthBits is the seven-bit payload-length field.
	wsFuzzLengthBits byte = 0x7F
	// wsFuzzLen16Marker in that field means "a 16-bit length follows".
	wsFuzzLen16Marker byte = 126
	// wsFuzzLen64Marker in that field means "a 64-bit length follows".
	wsFuzzLen64Marker byte = 127
	// wsFuzzLen16Width is how many bytes that 16-bit length occupies.
	wsFuzzLen16Width int = 2
	// wsFuzzLen64Width is how many bytes the 64-bit one occupies.
	wsFuzzLen64Width int = 8
)

// The close-code ranges RFC 6455 §7.4.1 and §7.4.2 define, transcribed for the
// same reason as the masks above.
const (
	// wsFuzzCloseLow is 1000, the first code the protocol itself defines.
	wsFuzzCloseLow uint16 = 1000
	// wsFuzzCloseRegisteredHigh is 1014, the last code registered against the
	// protocol block. 1015 sits above it and must never travel.
	wsFuzzCloseRegisteredHigh uint16 = 1014
	// wsFuzzCloseUndefined is 1004: reserved inside the block, never assigned.
	wsFuzzCloseUndefined uint16 = 1004
	// wsFuzzCloseNoStatus is 1005: "the Close frame carried no code", which is
	// a thing an application OBSERVES and never a thing a peer may send.
	wsFuzzCloseNoStatus uint16 = 1005
	// wsFuzzCloseAbnormal is 1006: "the connection died without a Close frame",
	// same rule.
	wsFuzzCloseAbnormal uint16 = 1006
	// wsFuzzCloseLibraryLow is 3000, where the first-come-first-served range
	// opens; it runs contiguously into the private range.
	wsFuzzCloseLibraryLow uint16 = 3000
	// wsFuzzClosePrivateHigh is 4999, where that contiguous range closes.
	wsFuzzClosePrivateHigh uint16 = 4999
)

// wsFuzzProbeMargin is how far past the widest legal header a fuzz case keeps
// probing prefixes.
//
// Two is enough to cover the prefix one byte longer than the widest header and
// the one after it, which is where a parser that used `>=` instead of `==`
// against its own announced length would first accept something.
const wsFuzzProbeMargin int = 2

// wsFuzzAnnouncedLen is the header width RFC 6455 §5.2 says the first two bytes
// announce, or 0 when there are not two bytes to read it from.
//
// It is the expectation WSFrameHeaderLen is judged against, so it is written
// from the diagram: two fixed bytes, plus the extended length the marker in the
// seven-bit field selects, plus four more when MASK is set.
func wsFuzzAnnouncedLen(b []byte) int {
	//: below two bytes the second byte does not exist, so nothing about the
	//: rest of the header is knowable yet.
	if len(b) < corenet.WSMinHeaderLen {
		//: the reader must come back with more.
		return 0
	}
	width := corenet.WSMinHeaderLen
	//: the seven-bit field is a length below 126 and a marker at or above it.
	switch b[1] & wsFuzzLengthBits {
	//: 126 selects the two-byte extended length.
	case wsFuzzLen16Marker:
		width += wsFuzzLen16Width
	//: 127 selects the eight-byte one.
	case wsFuzzLen64Marker:
		width += wsFuzzLen64Width
	}
	//: §5.1 puts the masking key last, after whichever length form was used.
	if b[1]&wsFuzzMaskBit != 0 {
		width += corenet.WSMaskLen
	}
	//: the exact width, which is also the exact number of bytes a reader may
	//: consume before it has bounded the payload.
	return width
}

// wsFuzzOpCodeDefined reports whether RFC 6455 §5.2 assigns the opcode a
// meaning: 0x0, 0x1 and 0x2 for data, 0x8, 0x9 and 0xA for control.
func wsFuzzOpCodeDefined(op byte) bool {
	//: everything else in the four-bit space — 0x3-0x7 and 0xB-0xF — is
	//: reserved, and reserved means the connection fails.
	switch op {
	//: the six the RFC names.
	case 0x0, 0x1, 0x2, 0x8, 0x9, 0xA:
		//: assigned.
		return true
	//: the ten it does not.
	default:
		//: reserved.
		return false
	}
}

// wsFuzzHeaderOracle is what RFC 6455 §5.2 and §5.5 say about a slice that is
// supposed to be one complete frame header, and nothing else.
//
// It returns the header the specification reads out of those bytes and whether
// the specification accepts them at all. Every rule is stated positively here —
// the slice is exactly the announced width, no RSV bit is set, the opcode is
// assigned, the length is spelled in the narrowest form that can carry it, a
// control frame is whole and short — so a parser that dropped one of them fails
// this oracle even though it still agrees with itself.
func wsFuzzHeaderOracle(head []byte) (want corenet.WSFrameHeaderValue, ok bool) {
	//: a header shorter than the two fixed bytes announces nothing.
	if len(head) < corenet.WSMinHeaderLen {
		//: nothing to accept.
		return corenet.WSFrameHeaderValue{}, false
	}
	width := wsFuzzAnnouncedLen(head)
	//: two rejections with one verdict. First: the slice must be the header and
	//: NOTHING else — a shorter one is a field that is not all there, a longer
	//: one is the payload or the next frame still attached, and a parser that
	//: took either would be reading bytes it has not bounded. Second, §5.2: an
	//: RSV bit means something only under a negotiated extension, and this
	//: domain negotiates none. The width test is left of the || so it
	//: short-circuits before head[0] is read on a slice that has no width.
	if len(head) != width || head[0]&wsFuzzRSVBits != 0 {
		//: neither sufficient nor necessary, or reserved bits set.
		return corenet.WSFrameHeaderValue{}, false
	}
	opcode := head[0] & wsFuzzOpCodeBits
	//: §5.2 — a reserved opcode fails the connection rather than being skipped.
	if !wsFuzzOpCodeDefined(opcode) {
		//: fail the connection.
		return corenet.WSFrameHeaderValue{}, false
	}
	length, lok := wsFuzzLengthOracle(head)
	//: a length the encoding cannot honestly express — non-minimal, or with the
	//: reserved most significant bit set.
	if !lok {
		//: fail the connection.
		return corenet.WSFrameHeaderValue{}, false
	}
	final := head[0]&wsFuzzFinBit != 0
	//: §5.5 — a control frame must be whole, because a peer cannot answer a
	//: fragment whose remainder it may never receive, and short, because the
	//: answer has to fit a buffer that is always available mid-message.
	if opcode&wsFuzzControlBit != 0 && (!final || length > uint64(corenet.WSMaxControlPayload)) {
		//: fail the connection.
		return corenet.WSFrameHeaderValue{}, false
	}
	parsed := corenet.WSFrameHeaderValue{
		Final:  final,
		OpCode: corenet.WSOpCode(opcode),
		Masked: head[1]&wsFuzzMaskBit != 0,
		Length: length,
	}
	//: §5.1 — the key is the last four bytes of the header when MASK is set.
	if parsed.Masked {
		copy(parsed.MaskKey[:], head[width-corenet.WSMaskLen:width])
	}
	//: a header the specification accepts, with the values it reads out of it.
	return parsed, true
}

// wsFuzzLengthOracle is §5.2's payload length, including the rule that makes
// the encoding unique: the MINIMAL number of bytes MUST be used.
//
// That rule is not decoration. A length-prefixed stream where 124 can be
// spelled three ways is a stream where two implementations can disagree about
// where the next frame starts, which is the one disagreement a framing layer
// cannot recover from.
func wsFuzzLengthOracle(head []byte) (length uint64, ok bool) {
	marker := head[1] & wsFuzzLengthBits
	//: below 126 the field IS the length, and there is only one spelling.
	if marker < wsFuzzLen16Marker {
		//: everything up to 125 bytes.
		return uint64(marker), true
	}
	//: 126 — the two bytes that follow the fixed pair.
	if marker == wsFuzzLen16Marker {
		value := uint64(binary.BigEndian.Uint16(head[corenet.WSMinHeaderLen:]))
		//: a value the seven-bit field could have carried is a second spelling
		//: of a frame that already has one.
		return value, value >= uint64(wsFuzzLen16Marker)
	}
	//: 127 — the eight bytes that follow.
	value := binary.BigEndian.Uint64(head[corenet.WSMinHeaderLen:])
	//: §5.2 reserves the most significant bit, so a length that sets it is not
	//: an enormous frame: it is a malformed one. And a value that fits sixteen
	//: bits is the same non-minimal spelling, one form up.
	return value, value <= math.MaxInt64 && value > math.MaxUint16
}

// wsFuzzLengthIsInItsClass is the BOUND, asserted independently of the oracle's
// own decode: a decoded length must be one that its own length field could have
// carried.
//
// It is a separate statement on purpose. The oracle proves the parser reads the
// same NUMBER the RFC does; this proves the number belongs to the field it came
// out of — which is what catches a parser that read the right bytes through the
// wrong width, or that fell through to a form the marker did not select.
func wsFuzzLengthIsInItsClass(second byte, length uint64) bool {
	//: which of the three forms the seven-bit field selected.
	switch marker := second & wsFuzzLengthBits; {
	//: the field is the length itself, so it is that value and no other.
	case marker < wsFuzzLen16Marker:
		//: and therefore never above 125.
		return length == uint64(marker)
	//: the 16-bit form carries 126 through 65535 and nothing outside it.
	case marker == wsFuzzLen16Marker:
		//: below 126 it is non-minimal; above 65535 it does not fit.
		return length >= uint64(wsFuzzLen16Marker) && length <= math.MaxUint16
	//: the 64-bit form carries 65536 through MaxInt64.
	default:
		//: below that it is non-minimal; above it the reserved bit is set.
		return length > math.MaxUint16 && length <= math.MaxInt64
	}
}

// wsFuzzProbeLengths lists the prefix lengths one fuzz case checks.
//
// The sweep is what turns "the announced width is sufficient" and "it is
// necessary" into one statement: ParseWSFrameHeader must accept AT MOST the
// prefix whose length WSFrameHeaderLen announced, and must refuse every other
// prefix of the same bytes. The window stops a little past the widest legal
// header because a frame's payload can be gigabytes and probing every prefix of
// it would spend the whole fuzz budget re-refusing the same shape — but the
// FULL slice is probed too, because a header with its payload still attached is
// exactly the input a reader hands in when it has over-read.
func wsFuzzProbeLengths(b []byte) []int {
	limit := min(len(b), corenet.WSMaxHeaderLen+wsFuzzProbeMargin)
	lengths := make([]int, 0, limit+wsFuzzProbeMargin)
	for m := range limit + 1 {
		lengths = append(lengths, m)
	}
	//: the whole slice, when it reaches past the window.
	if len(b) > limit {
		lengths = append(lengths, len(b))
	}
	//: every prefix worth asking about, each asked exactly once.
	return lengths
}

// wsFuzzFrameSeeds is the corpus the frame-header target starts from.
//
// It is not a sample: it is one entry per BRANCH of the format, plus the
// degenerate inputs that only arrive from a real socket. Each length class
// appears masked and unmasked, because the mask bit moves the key to a
// different offset in each of them; each truncation lands INSIDE a field rather
// than at a field boundary, because a parser that checked its bounds per field
// instead of per header fails only there.
func wsFuzzFrameSeeds() [][]byte {
	return [][]byte{
		//: nothing at all, and the one byte a reader has after a single read.
		{},
		{0x81},
		//: the 7-bit class, both directions. The masked one is RFC 6455 §5.7's
		//: own worked example.
		{0x81, 0x05},
		{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d},
		//: the 7-bit boundary: 125 is the last length the field carries, and a
		//: control frame is allowed exactly that much.
		{0x89, 0x7D},
		{0x81, 0x7D},
		//: the 16-bit class, both directions, at 256 bytes.
		{0x82, 0x7E, 0x01, 0x00},
		{0x82, 0xFE, 0x01, 0x00, 0x01, 0x02, 0x03, 0x04},
		//: the 16-bit boundaries: the smallest value the form may carry and the
		//: largest.
		{0x82, 0x7E, 0x00, 0x7E},
		{0x82, 0x7E, 0xFF, 0xFF},
		//: the 64-bit class, both directions, at 65536 bytes.
		{0x82, 0x7F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00},
		{0x82, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0xAA, 0xBB, 0xCC, 0xDD},
		//: truncated INSIDE the 16-bit length, and inside the 64-bit one.
		{0x82, 0x7E, 0x01},
		{0x82, 0x7F, 0x00, 0x00},
		//: truncated inside the masking key of a 7-bit masked frame.
		{0x81, 0x85, 0x37, 0xfa},
		//: the 64-bit length with the reserved most significant bit set.
		{0x82, 0x7F, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		//: and with every bit set, which is the same rule seen from the top.
		{0x82, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01, 0x02, 0x03, 0x04},
		//: a length that declares 65535 bytes from a four-byte buffer — the
		//: input that separates "what the peer says" from "what it sent".
		{0x81, 0x7E, 0xFF, 0xFF},
		//: control frames past the 125-byte ceiling, in both length forms.
		{0x89, 0x7E, 0x00, 0x7E},
		{0x89, 0xFE, 0x00, 0x7F, 0x01, 0x02, 0x03, 0x04},
		{0x88, 0x7F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00},
		//: a fragmented control frame, which no peer could ever answer.
		{0x08, 0x80, 0x00, 0x00, 0x00, 0x00},
		//: RSV1 set — byte for byte what a permessage-deflate frame looks like
		//: to an endpoint that negotiated no extension.
		{0xC1, 0x80, 0x00, 0x00, 0x00, 0x00},
		//: a reserved data opcode and a reserved control one.
		{0x83, 0x80, 0x00, 0x00, 0x00, 0x00},
		{0x8B, 0x80, 0x00, 0x00, 0x00, 0x00},
		//: §5.2's own counter-example for the minimal-encoding rule: 124
		//: spelled in the 16-bit form.
		{0x81, 0xFE, 0x00, 0x7C, 0x00, 0x00, 0x00, 0x00},
		//: 125 spelled in the 16-bit form — the LAST value the seven-bit field
		//: can carry, and therefore the one an off-by-one in the minimal-
		//: encoding bound lets through. These two entries are not invented:
		//: they are the inputs the fuzzer minimised to when that bound was
		//: deliberately written `< 126-1`, promoted here verbatim so the same
		//: defect is caught in five milliseconds without a search.
		{0x81, 0xFE, 0x00, 0x7D, 0x30, 0x30, 0x30, 0x30},
		{0x89, 0xFE, 0x00, 0x7D, 0x30, 0x30, 0x30, 0x30},
		//: and the same boundary one form up: 65535 spelled in 64 bits, where
		//: the 16-bit form ends.
		{0x82, 0x7F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF},
		//: and the same rule one form up: 256 spelled in 64 bits.
		{0x82, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00},
		//: a masked Close frame carrying a two-byte status code.
		{0x88, 0x82, 0x00, 0x00, 0x00, 0x00},
		//: a complete header with its payload still attached, which a reader
		//: that over-read hands in verbatim.
		{0x81, 0x03, 'a', 'b', 'c'},
	}
}

// FuzzParseWSFrameHeader drives the header parser from bytes nobody chose and
// judges every answer against RFC 6455 rather than against the parser.
//
// Four properties are asserted on every input, and each exists because a
// different class of defect passes the other three:
//
//   - VERDICT. ParseWSFrameHeader accepts exactly what wsFuzzHeaderOracle
//     accepts, and on acceptance returns exactly the value the oracle reads.
//     A parser that refused everything fails this; so does one that accepts a
//     non-minimal length or a reserved opcode.
//
//   - EXACTNESS. Over a sweep of prefix lengths, the ONLY prefix that may be
//     accepted is the one WSFrameHeaderLen announced. Shorter is a field that
//     is not all there; longer is payload the parser has not bounded. This is
//     what pins WSFrameHeaderLen and ParseWSFrameHeader to the same number —
//     they are used as a pair by a reader that takes exactly what the first
//     one says, and a disagreement between them is a read past the header.
//
//   - BOUND. A decoded length belongs to the class its own length field
//     selected, and a control frame never carries more than
//     WSMaxControlPayload. Asserted separately from the oracle, so a decode
//     through the wrong width is caught even where the oracle would agree.
//
//   - PURITY. Two calls on the same bytes give the same answer, and the input
//     buffer is byte-for-byte unchanged afterwards. A parser that wrote into
//     the caller's buffer would corrupt the read buffer of every connection
//     that reuses one, which is every connection here.
//
// # Mutation-checked
//
// The minimal-encoding bound in parseWSLength16 was deliberately shifted by one
// — `value < uint64(wsLength16Marker)` rewritten as `< uint64(wsLength16Marker)-1`
// — so that a 16-bit length field spelling 125 was accepted instead of refused.
//
// EVERY test this package already had passed with that defect in place,
// `go test ./internal/core/net` included, and so did this target's own seed
// corpus as it stood before the two `00 7D` entries above were added: a table
// author writes 124 because §5.2 prints 124, and 125 is the value the rule is
// actually about. The search found it three times out of three from a cleared
// fuzz cache, in 0.21 s, 1.73 s and 0.10 s, minimising to
//
//	ParseWSFrameHeader(89 fe 00 7d 30 30 30 30) err = <nil>; RFC 6455 accepts it = false (announced width 8)
//
// and, on the third run, to the same eight bytes with a Text opcode. That is
// the whole argument for fuzzing a length-prefixed parser: the inputs that
// matter are the ones on the boundary between two spellings of the same frame,
// and nobody writes those down.
func FuzzParseWSFrameHeader(f *testing.F) {
	//: one seed per branch of the format, plus the degenerate inputs.
	for _, seed := range wsFuzzFrameSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		//: the parser is handed a buffer the caller still owns; PURITY is
		//: checked against this copy at the end.
		pristine := slices.Clone(b)
		announced := corenet.WSFrameHeaderLen(b)
		//: WSFrameHeaderLen is the number a reader sizes its next read from, so
		//: it is judged first and on its own.
		if want := wsFuzzAnnouncedLen(b); announced != want {
			t.Fatalf("WSFrameHeaderLen(% x) = %d, want %d", b, announced, want)
		}
		//: EXACTNESS — every prefix, including the announced one, judged by the
		//: same oracle. The oracle refuses any slice whose length is not the
		//: announced width, so this single loop states both halves: the width
		//: is sufficient, and every other width is refused.
		for _, m := range wsFuzzProbeLengths(b) {
			head := b[:m]
			want, ok := wsFuzzHeaderOracle(head)
			header, err := corenet.ParseWSFrameHeader(head)
			//: VERDICT, both ways — accepting what the RFC forbids and refusing
			//: what it allows are the same bug seen from two sides.
			if ok != (err == nil) {
				t.Fatalf("ParseWSFrameHeader(% x) err = %v; RFC 6455 accepts it = %v (announced width %d)",
					head, err, ok, announced)
			}
			//: a refusal is a framing violation and must say so: the code is
			//: what decides the close status the peer is sent.
			if !ok {
				if !errs.HasCode(err, corenet.CodeWSProtocolViolation) {
					t.Fatalf("ParseWSFrameHeader(% x) refused with code %v, want WS_PROTOCOL_VIOLATION",
						head, wsCodeOf(err))
				}
				continue
			}
			if header != want {
				t.Fatalf("ParseWSFrameHeader(% x) = %+v, want %+v", head, header, want)
			}
			//: BOUND — the length belongs to the field that selected it.
			if !wsFuzzLengthIsInItsClass(head[1], header.Length) {
				t.Fatalf("ParseWSFrameHeader(% x) length %d is outside the class marker %d selects",
					head, header.Length, head[1]&wsFuzzLengthBits)
			}
			//: and the ceiling §5.5 puts on a frame that must be answerable
			//: inline, restated here because it is the one bound whose failure
			//: is a buffer a peer chose the size of.
			if header.OpCode.IsControl() && header.Length > uint64(corenet.WSMaxControlPayload) {
				t.Fatalf("ParseWSFrameHeader(% x) accepted a %s frame carrying %d bytes, ceiling is %d",
					head, header.OpCode, header.Length, corenet.WSMaxControlPayload)
			}
			//: PURITY, first half — the same bytes must give the same answer.
			again, aerr := corenet.ParseWSFrameHeader(head)
			if aerr != nil || again != header {
				t.Fatalf("ParseWSFrameHeader(% x) is not deterministic: %+v/%v then %+v/%v",
					head, header, err, again, aerr)
			}
		}
		//: PURITY, second half — nothing above may have written into the input.
		if !bytes.Equal(b, pristine) {
			t.Fatalf("the parser wrote into its input: % x, was % x", b, pristine)
		}
	})
}

// wsFuzzCloseSendable is §7.4.1 and §7.4.2's registry: which status codes may
// appear in a Close frame on the wire.
//
// The three holes are the point. 1004 was reserved and never assigned; 1005 and
// 1006 describe the ABSENCE of a Close frame, so a peer that sends one is
// contradicting its own delivery; and 1015 sits above the registered block
// entirely, for a TLS handshake that failed before there was a connection to
// say so on.
func wsFuzzCloseSendable(code uint16) bool {
	//: the protocol block, minus its three holes.
	if code >= wsFuzzCloseLow && code <= wsFuzzCloseRegisteredHigh {
		//: 1004 was never assigned; 1005 and 1006 are observations, not codes.
		return code != wsFuzzCloseUndefined && code != wsFuzzCloseNoStatus && code != wsFuzzCloseAbnormal
	}
	//: the library range and the private one are contiguous and both open;
	//: everything else — under 1000, the gap that swallows 1015, past 4999 —
	//: is reserved and unallocated.
	return code >= wsFuzzCloseLibraryLow && code <= wsFuzzClosePrivateHigh
}

// wsFuzzCloseOracle is what RFC 6455 §5.5.1 says a received Close payload
// means, and which refusal it earns when it means nothing.
//
// The refusal is returned as the error CODE rather than as a bool, because the
// two refusals are not interchangeable: a malformed shape closes the connection
// with 1002 and a reason that is not UTF-8 closes it with 1007, and a parser
// that returned the wrong one would have the peer close for the wrong reason.
func wsFuzzCloseOracle(b []byte) (code corenet.WSCloseCode, reason string, refusal errs.Code) {
	//: §5.5.1 — a Close frame may carry no payload at all, and that is not an
	//: error: it is the peer closing without saying why.
	if len(b) == 0 {
		//: the value that exists to describe exactly this absence.
		return corenet.WSCloseNoStatus, "", 0
	}
	//: one byte is not half a status code. Salvaging it would let the peer
	//: choose which half of its own code this endpoint reads.
	if len(b) < corenet.WSCloseCodeLen {
		//: a framing violation.
		return 0, "", corenet.CodeWSProtocolViolation
	}
	received := binary.BigEndian.Uint16(b[:corenet.WSCloseCodeLen])
	//: the registry governs what is received exactly as it governs what is
	//: sent — a peer sending 1006 commits the error we refuse to commit.
	if !wsFuzzCloseSendable(received) {
		//: a framing violation.
		return 0, "", corenet.CodeWSProtocolViolation
	}
	text := b[corenet.WSCloseCodeLen:]
	//: §5.5.1 makes the reason UTF-8 and §8.1 makes invalid UTF-8 fatal.
	if !utf8.Valid(text) {
		//: a payload violation, which is 1007 and not 1002.
		return 0, "", corenet.CodeWSInvalidPayload
	}
	//: the peer's code and the reason it gave.
	return corenet.WSCloseCode(received), string(text), 0
}

// wsFuzzCloseSeeds is the corpus the Close-payload target starts from: one
// entry per branch of §5.5.1, plus every boundary of the code registry.
func wsFuzzCloseSeeds() [][]byte {
	//: 120 bytes of reason puts the payload at the 125-byte ceiling once the
	//: two-byte code is in front of it; 121 puts it one past.
	atCeiling := append([]byte{0x03, 0xE8}, bytes.Repeat([]byte{'x'}, corenet.WSMaxControlPayload-corenet.WSCloseCodeLen)...)
	pastCeiling := append(slices.Clone(atCeiling), 'x')
	return [][]byte{
		//: no payload at all, which is legal and means "no status".
		{},
		//: one byte, which is not.
		{0x03},
		//: 1000 alone, and 1001 with a reason.
		{0x03, 0xE8},
		{0x03, 0xE9, 'b', 'y', 'e'},
		//: the hole at 1004 and the three codes that describe an absence.
		{0x03, 0xEC},
		{0x03, 0xED},
		{0x03, 0xEE},
		{0x03, 0xF7},
		//: the boundaries of the protocol block, from below and from above.
		{0x03, 0xE7},
		{0x03, 0xF6},
		//: the library range's first code and the private range's last, plus
		//: the first value past it.
		{0x0B, 0xB8},
		{0x13, 0x87},
		{0x13, 0x88},
		//: 2999, the last value BELOW the library range. It is here because a
		//: seed list written by hand stops at the boundary value itself — the
		//: fuzzer minimised to these two bytes in 1.3 s when that lower bound
		//: was deliberately written `>= 3000-1`, and the seeds as they stood
		//: did not notice.
		{0x0B, 0xB7},
		//: the extremes of the sixteen-bit space.
		{0x00, 0x00},
		{0xFF, 0xFF},
		//: a reason that is not UTF-8, and one that is valid multibyte.
		{0x03, 0xE8, 0xFF, 0xFE},
		{0x03, 0xE8, 0xC3, 0xA9},
		//: a reason truncated in the middle of a two-byte rune, which is what
		//: a peer that split its own message on a byte boundary produces.
		{0x03, 0xE8, 0xC3},
		//: a lone continuation byte and an over-long encoding of NUL, the two
		//: shapes a UTF-8 validator written as a length table accepts.
		{0x03, 0xE8, 0x80},
		{0x03, 0xE8, 0xC0, 0x80},
		//: the control-frame ceiling, exactly on it and one byte past.
		atCeiling,
		pastCeiling,
	}
}

// FuzzParseWSClosePayload drives the Close-payload parser the same way, and
// adds the property the frame header has no equivalent of: a round trip.
//
// Three properties, each covering what the others miss:
//
//   - VERDICT. The parser accepts exactly what wsFuzzCloseOracle accepts,
//     returns the same code and the same reason, and on refusal carries the
//     SAME error code — 1002 for a shape the RFC forbids, 1007 for a reason
//     that is not UTF-8. Those two are not interchangeable: they decide what
//     this endpoint tells the peer went wrong.
//
//   - VERBATIM. The reason handed back is byte-for-byte the bytes after the
//     code. A reason that came back truncated, re-encoded or with a
//     replacement character in it would be a different sentence than the peer
//     sent, delivered to the application as if it were theirs.
//
//   - ROUND TRIP. Whatever this endpoint accepted, it must be able to say
//     again: AppendWSClosePayload must rebuild the exact bytes for every
//     accepted payload that still fits a control frame, and must refuse the
//     ones that do not. That is the one assertion the oracle cannot make on
//     its own — it compares the parser against the WRITER, so a shared
//     misreading of the code registry shows up as a payload that parses and
//     cannot be re-emitted.
//
// # Mutation-checked
//
// The lower bound of the library range in Sendable was deliberately shifted by
// one — `c >= wsCloseLibraryLow` rewritten as `>= wsCloseLibraryLow-1` — so
// that 2999, a code §7.4.2 leaves reserved and unallocated, was accepted from a
// peer. The seed corpus as it stood did not notice, because a hand-written list
// stops AT the boundary and 3000 was already in it. The search minimised to the
// two bytes below it in 1.3 s:
//
//	ParseWSClosePayload(0b b7) = (2999, "", nil); RFC 6455 refuses it with 0.2.11.29
//
// That input is a seed above now, and it fails against that same mutation in
// four milliseconds without a search.
func FuzzParseWSClosePayload(f *testing.F) {
	//: one seed per branch of §5.5.1 and per boundary of the registry.
	for _, seed := range wsFuzzCloseSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		//: the payload belongs to the connection's read buffer; the parser may
		//: not write into it.
		pristine := slices.Clone(b)
		wantCode, wantReason, refusal := wsFuzzCloseOracle(b)
		code, reason, err := corenet.ParseWSClosePayload(b)
		//: PURITY FIRST, so it covers the REJECTION path as well as the
		//: acceptance one. A parser that decoded half a payload into the
		//: caller's buffer and only then refused it would otherwise slip
		//: through: the refusal branch below returns early, and every
		//: malformed input — which is most of the corpus — would never reach
		//: a purity check placed after it.
		againCode, againReason, againErr := corenet.ParseWSClosePayload(b)
		//: the same bytes must yield the same verdict, refusal included.
		if (againErr == nil) != (err == nil) || againCode != code || againReason != reason {
			//: a parser whose answer depends on hidden state.
			t.Fatalf("ParseWSClosePayload(% x) is not deterministic: (%d, %q, %v) then (%d, %q, %v)",
				b, code, reason, err, againCode, againReason, againErr)
		}
		//: and neither call may have written into the input.
		if !bytes.Equal(b, pristine) {
			//: the parser mutated a buffer it does not own.
			t.Fatalf("the parser wrote into its input: % x, was % x", b, pristine)
		}
		//: VERDICT — the refusal, and which refusal it is.
		if refusal != 0 {
			if err == nil {
				t.Fatalf("ParseWSClosePayload(% x) = (%d, %q, nil); RFC 6455 refuses it with %v",
					b, code, reason, refusal)
			}
			if !errs.HasCode(err, refusal) {
				t.Fatalf("ParseWSClosePayload(% x) refused with code %v, want %v", b, wsCodeOf(err), refusal)
			}
			//: nothing else is knowable about a refused payload.
			return
		}
		if err != nil {
			t.Fatalf("ParseWSClosePayload(% x) = %v; RFC 6455 accepts it as (%d, %q)", b, err, wantCode, wantReason)
		}
		if code != wantCode || reason != wantReason {
			t.Fatalf("ParseWSClosePayload(% x) = (%d, %q), want (%d, %q)", b, code, reason, wantCode, wantReason)
		}
		//: the one code this endpoint may hold and must never send is also the
		//: only unsendable one it may report — and only for an empty payload.
		if !code.Sendable() && (len(b) != 0 || code != corenet.WSCloseNoStatus) {
			t.Fatalf("ParseWSClosePayload(% x) returned %d, which must not travel", b, code)
		}
		//: VERBATIM — the reason is the bytes after the code, unaltered.
		if len(b) >= corenet.WSCloseCodeLen && !bytes.Equal([]byte(reason), b[corenet.WSCloseCodeLen:]) {
			t.Fatalf("ParseWSClosePayload(% x) reason = % x, want % x", b, reason, b[corenet.WSCloseCodeLen:])
		}
		//: ROUND TRIP — only the empty payload has no code to write back.
		if len(b) != 0 {
			wsFuzzAssertCloseRoundTrip(t, b, code, reason)
		}
	})
}

// wsFuzzAssertCloseRoundTrip asserts that a payload this endpoint accepted is
// one it can also emit, byte for byte, whenever it still fits a control frame.
//
// The ceiling is where the two halves legitimately part company: §5.5 caps a
// control frame at the payload width named by corenet.WSMaxControlPayload, so a
// longer payload is one a conforming peer could not have sent — the writer must
// refuse it even though the reader, whose job is to say what the bytes mean,
// has already read it.
func wsFuzzAssertCloseRoundTrip(t *testing.T, b []byte, code corenet.WSCloseCode, reason string) {
	t.Helper()
	wire, err := corenet.AppendWSClosePayload(nil, code, reason)
	//: past the ceiling the writer must refuse, and must leave the destination
	//: exactly as it found it.
	if len(b) > corenet.WSMaxControlPayload {
		if err == nil {
			t.Fatalf("AppendWSClosePayload(%d, %q) built %d bytes; the control-frame ceiling is %d",
				code, reason, len(wire), corenet.WSMaxControlPayload)
		}
		//: refused as it must be.
		return
	}
	if err != nil {
		t.Fatalf("AppendWSClosePayload(%d, %q) = %v, but ParseWSClosePayload accepted those same %d bytes",
			code, reason, err, len(b))
	}
	if !bytes.Equal(wire, b) {
		t.Fatalf("AppendWSClosePayload(%d, %q) = % x, want % x", code, reason, wire, b)
	}
}
