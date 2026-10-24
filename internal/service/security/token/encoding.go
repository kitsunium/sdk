package token

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"strings"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// maxSegments is the largest dot-separated part count any format here
	// uses: PASETO with a footer. JWS compact uses three.
	maxSegments int = 4
	// le64Width is the octet width of PASETO's LE64 length prefix.
	le64Width int = 8
	// le64SignMask clears the most significant bit, as PASETO's LE64 requires,
	// so the encoding stays unambiguous where integers are signed.
	le64SignMask uint64 = 0x7FFF_FFFF_FFFF_FFFF
)

// b64 is the ONLY base64 alphabet this package accepts: unpadded base64url
// (RFC 7515 §2), in Strict mode.
//
// Strict matters. Without it, Go accepts a final quantum whose unused bits are
// non-zero, so two different strings decode to the same octets — and a token is
// a string somebody may have already keyed a cache, a replay table or a
// revocation list on. One encoding, one spelling.
var b64 = base64.RawURLEncoding.Strict()

// segmentsValue is a fixed-capacity view over a compact token's dot-separated
// parts. It is an ARRAY, not a slice, and that is the whole point.
//
// CVE-2025-30204 was a strings.Split on the token before any length check: an
// input of a few megabytes of "." made the split allocate a slice header per
// separator, so a request the size of a photo became hundreds of megabytes of
// garbage. Here the bound is checked first, the count cannot exceed
// maxSegments, and the parts are sub-slices of the caller's string — the split
// allocates nothing at all.
type segmentsValue struct {
	// part holds up to maxSegments views into the original token string.
	part [maxSegments]string
	// count is how many entries of part are populated.
	count int
}

// splitCompact splits tok on '.' into at most maxSegments parts.
//
// It refuses a token longer than maxLen BEFORE scanning, so the scan itself is
// bounded by a number the caller configured rather than one the attacker sent,
// and it refuses a token with more than maxSegments parts the moment the extra
// separator is seen rather than after counting them all.
func splitCompact(tok string, maxLen int) (seg segmentsValue, err error) {
	//: the size bound comes first — everything below is work this check funds.
	if len(tok) > maxLen {
		//: refuse without looking at a single byte of the payload.
		return segmentsValue{}, errs.Wrap(coretoken.TooLarge, errs.WrapParams{},
			errs.Int("limit", maxLen))
	}
	rest := tok
	//: walk separator to separator; each iteration consumes at least one byte.
	for {
		idx := strings.IndexByte(rest, '.')
		//: no separator left — the remainder is the final segment.
		if idx < 0 {
			//: room for the last part is guaranteed by the check below.
			seg.part[seg.count] = rest
			seg.count++
			//: a complete, bounded split.
			return seg, nil
		}
		//: one more separator than the format allows: stop here, not after
		//: counting every '.' in a megabyte of them.
		if seg.count == maxSegments-1 {
			//: too many parts is a structural failure, not a size one.
			return segmentsValue{}, coretoken.Malformed
		}
		//: store the view and advance past the separator.
		seg.part[seg.count] = rest[:idx]
		seg.count++
		rest = rest[idx+1:]
	}
}

// decodeSegment decodes one unpadded base64url segment, refusing anything
// whose decoded form would exceed maxLen before allocating the buffer.
func decodeSegment(segment string, maxLen int) (raw []byte, err error) {
	//: DecodedLen is exact for RawURLEncoding, so this is the real size, and
	//: checking it first is what stops a large segment from being allocated
	//: before it is rejected.
	if b64.DecodedLen(len(segment)) > maxLen {
		//: refuse before allocating anything the size of the answer.
		return nil, errs.Wrap(coretoken.TooLarge, errs.WrapParams{},
			errs.Int("limit", maxLen))
	}
	//: strict decode — padding, the standard alphabet and non-zero trailing
	//: bits are all rejected rather than accommodated.
	decoded, derr := b64.DecodeString(segment)
	//: any decode fault makes this not a token.
	if derr != nil {
		//: base64.CorruptInputError names a position in attacker-controlled
		//: bytes, so it is not carried — see malformed's own comment.
		return nil, malformed("segment is not strict unpadded base64url")
	}
	//: exactly the decoded octets.
	return decoded, nil
}

// splitCompactBytes is splitCompact over a token held as bytes — the form a
// caller verifying straight out of a receive buffer has. It refuses exactly
// what splitCompact refuses, in the same order, and its parts are views of
// tok: the split allocates nothing.
func splitCompactBytes(tok []byte, maxLen int) (seg segmentsBytes, err error) {
	//: the size bound comes first — everything below is work this check funds.
	if len(tok) > maxLen {
		//: refuse without looking at a single byte of the payload.
		return segmentsBytes{}, errs.Wrap(coretoken.TooLarge, errs.WrapParams{},
			errs.Int("limit", maxLen))
	}
	rest := tok
	//: walk separator to separator; each iteration consumes at least one byte.
	for {
		idx := bytes.IndexByte(rest, '.')
		//: no separator left — the remainder is the final segment.
		if idx < 0 {
			//: room for the last part is guaranteed by the check below.
			seg.part[seg.count] = rest
			seg.count++
			//: a complete, bounded split.
			return seg, nil
		}
		//: one more separator than the format allows.
		if seg.count == maxSegments-1 {
			//: too many parts is a structural failure, not a size one.
			return segmentsBytes{}, coretoken.Malformed
		}
		//: store the view and advance past the separator.
		seg.part[seg.count] = rest[:idx]
		seg.count++
		rest = rest[idx+1:]
	}
}

// segmentsBytes is segmentsValue over a token held as bytes.
type segmentsBytes struct {
	// part holds up to maxSegments views into the original token.
	part [maxSegments][]byte
	// count is how many entries of part are populated.
	count int
}

// decodeSegmentInto is decodeSegment into the caller's buffer: the same
// bounds and the same strict alphabet, refused in the same order. dst is used
// when the decoded segment fits it, so a caller handing a stack array decodes
// without allocating; a larger segment — still inside maxLen — gets a buffer
// of its own.
func decodeSegmentInto(dst, segment []byte, maxLen int) (raw []byte, err error) {
	size := b64.DecodedLen(len(segment))
	//: DecodedLen is exact for RawURLEncoding: refuse before decoding.
	if size > maxLen {
		//: refuse before allocating anything the size of the answer.
		return nil, errs.Wrap(coretoken.TooLarge, errs.WrapParams{},
			errs.Int("limit", maxLen))
	}
	//: the caller's buffer when it is large enough.
	if size > len(dst) {
		dst = make([]byte, size)
	}
	n, derr := b64.Decode(dst[:size], segment)
	//: any decode fault makes this not a token.
	if derr != nil {
		//: base64.CorruptInputError names a position in attacker-controlled
		//: bytes, so it is not carried.
		return nil, malformed("segment is not strict unpadded base64url")
	}
	//: exactly the decoded octets.
	return dst[:n], nil
}

// checkJSONDepth refuses raw when its bracket nesting exceeds maxDepth.
//
// It runs BEFORE the scanner descends a level, because the cheapest place to
// refuse a payload of ten thousand open brackets is a linear scan with one
// integer of state — not a reader that has already recursed ten thousand
// times. It also fixes which verdict a too-deep payload gets: TooDeep, before
// anything the scan would refuse later in the bytes.
// The scan is string-aware so a '{' inside a claim value is data, not depth.
func checkJSONDepth(raw []byte, maxDepth int) error {
	scan := depthScan{}
	//: single pass, three fields of state.
	for _, char := range raw {
		//: inside a JSON string, brackets are data.
		if scan.inString {
			scan.consumeStringByte(char)
			continue
		}
		//: outside a string, only four bytes matter.
		scan.consumeStructuralByte(char)
		//: refuse at the first byte that crosses the bound.
		if scan.depth > maxDepth {
			//: name the limit, never the payload.
			return errs.Wrap(coretoken.TooDeep, errs.WrapParams{},
				errs.Int("limit", maxDepth))
		}
	}
	//: nesting stayed inside the bound.
	return nil
}

// preAuthEncode implements PASETO's PAE (pre-authentication encoding): the
// piece count as LE64, then each piece's length as LE64 followed by the piece.
//
// It is what makes a PASETO signature cover the version header and the footer
// as well as the payload, and it is injective — no two different piece lists
// encode to the same bytes — which is what stops a byte moving from the
// payload into the footer without changing the signature.
func preAuthEncode(pieces ...[]byte) []byte {
	total := le64Width
	//: size the buffer exactly: one length prefix per piece plus each piece.
	for _, piece := range pieces {
		total += le64Width + len(piece)
	}
	out := make([]byte, 0, total)
	//: the count comes first.
	out = appendLE64(out, uint64(len(pieces)))
	//: then every piece, length-prefixed.
	for _, piece := range pieces {
		out = appendLE64(out, uint64(len(piece)))
		out = append(out, piece...)
	}
	//: the pre-authentication input the signature covers.
	return out
}

// appendLE64 appends n as eight little-endian bytes with the most significant
// bit cleared, as PASETO's LE64 specifies.
func appendLE64(dst []byte, n uint64) []byte {
	var buf [le64Width]byte
	//: clear the top bit before encoding, per the PASETO LE64 definition.
	binary.LittleEndian.PutUint64(buf[:], n&le64SignMask)
	//: append the fixed-width prefix.
	return append(dst, buf[:]...)
}
