package http2

import (
	"bytes"
	"strconv"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The response framing's values, mirroring http1's writer.
// extraFieldMax is the room for a Retry-After or X-Request-Id line.
const extraFieldMax int = 128

const (
	// statusOK is what a zero HTTP1ResponseValue.Status means.
	statusOK int = 200
	// statusNoContent carries neither a length nor a body.
	statusNoContent int = 204
	// statusNotModified carries neither a length nor a body.
	statusNotModified int = 304
	// statusMin is the lowest status the profile sends.
	statusMin int = 200
	// statusMax is the highest three-digit status.
	statusMax int = 999
	// neverIndexedByte opens a never-indexed literal, RFC 7541 §6.2.3: four
	// high bits, then the name's index in a 4-bit integer.
	neverIndexedByte byte = 0x10
	// prefix4Max is the largest index a 4-bit integer holds in one byte.
	prefix4Max int = 15
	// statusNameIndex is :status's name index in the static table, Appendix A.
	statusNameIndex int = 8
	// firstStaticStatus is the static index of the first of staticStatusCodes.
	firstStaticStatus int = 8
	// contentLengthIndex is content-length's name index in the static table.
	contentLengthIndex int = 28
	// statusFieldLen is a three-digit status: the value's length in bytes.
	statusFieldLen byte = 3
	// maxNameLen bounds a response field's name.
	maxNameLen int = 4096
	// decimal is the base a status and a content length are written in.
	decimal int = 10
	// stringLenThreshold is the largest string length a 7-bit length integer
	// holds in one byte.
	stringLenThreshold int = 127
	// asciiColonByte separates a header line's name and value.
	asciiColonByte byte = ':'
	// toLower is the bit that folds an ASCII capital to lowercase.
	toLower byte = 0x20
)

var (
	// tokenBits is the RFC 9110 §5.6.2 token characters as a bit set, one
	// word per 64 bytes.
	tokenBits = [4]uint64{0x03ff6cfa00000000, 0x57ffffffc7fffffe, 0x0000000000000000, 0x0000000000000000}
	// staticStatusCodes are the seven statuses the static table holds, RFC 7541
	// Appendix A, in index order from firstStaticStatus.
	staticStatusCodes = [...]uint16{200, 204, 206, 304, 400, 404, 500}
	// connectionFields are RFC 9113 §8.2.2's connection-specific names, which
	// an HTTP/2 message never carries, and content-length, which the encoder
	// writes itself: a response's copies are dropped.
	connectionFields = [...]string{
		"connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "content-length",
	}
)

// Encoder writes response field blocks: no dynamic table, no Huffman coding, so
// a peer's HEADER_TABLE_SIZE needs no honouring beyond zero.
type Encoder struct {
	// maxFrame is the largest frame the peer accepts.
	maxFrame int
}

// NewEncoder returns a response encoder writing at most maxFrame bytes a frame,
// the MAX_FRAME_SIZE the peer accepts.
func NewEncoder(maxFrame int) Encoder {
	return Encoder{maxFrame: maxFrame}
}

// AppendHeaders appends a HEADERS frame answering stream: :status, then
// res.Header's lines re-read as never-indexed literals with lowercased names
// (connection-specific fields dropped, RFC 9113 §8.2.2), then content-length
// from len(res.Body) unless a 204 or a 304. END_STREAM is set when endStream.
// A block past the peer's frame size is split over CONTINUATION frames. A
// status outside 200..999, a body on a 204 or 304 or a malformed Header line
// is refused with corenet.HTTP1ResponseInvalid, nothing appended. It
// allocates nothing when dst has room.
func (e Encoder) AppendHeaders(dst []byte, stream uint32, res *corenet.HTTP1ResponseValue, endStream bool) ([]byte, error) {
	status := cmpInt(res.Status, statusOK)
	//: a status the profile does not send, or a body on a bodiless status.
	if !sendableStatus(status, res) {
		return dst, corenet.HTTP1ResponseInvalid
	}
	start := len(dst)
	dst = AppendFrameHeader(dst, corehttp2.FrameValue{})
	blockStart := len(dst)
	dst = appendStatus(dst, status)
	dst, ok := appendResponseFields(dst, res.Header)
	//: a malformed line: nothing of this response is appended.
	if !ok {
		return dst[:start], corenet.HTTP1ResponseInvalid
	}
	//: what http1's writer adds after Header: the CORS fields, the delay, the identifier.
	var fok bool
	if dst, fok = appendExtraFields(dst, res); !fok {
		return dst[:start], corenet.HTTP1ResponseInvalid
	}
	//: the length the body takes, except on the two statuses that carry none.
	if status != statusNoContent && status != statusNotModified {
		dst = appendContentLength(dst, len(res.Body))
	}
	return e.frameBlock(dst, start, blockStart, stream, endStream), nil
}

// cmpInt returns v unless it is zero, then def.
func cmpInt(v, def int) int {
	//: zero is "the default".
	if v == 0 {
		return def
	}
	return v
}

// sendableStatus mirrors AppendResponse: a status of the profile, no body on
// a status that carries none.
func sendableStatus(status int, res *corenet.HTTP1ResponseValue) bool {
	bodiless := status == statusNoContent || status == statusNotModified
	//: outside 200..999, an interim response or a code outside the profile,
	//: or a body on a 204 or a 304.
	if status < statusMin || status > statusMax || (bodiless && len(res.Body) > 0) {
		return false
	}
	return true
}

// appendStatus appends the :status field.
func appendStatus(dst []byte, status int) []byte {
	//: one of the static table's seven statuses is a single indexed byte.
	for i, code := range staticStatusCodes {
		//: a status the static table holds takes no literal.
		if int(code) == status {
			return append(dst, indexedPrefix|byte(firstStaticStatus+i))
		}
	}
	dst = appendIndexedLiteralName(dst, statusNameIndex)
	dst = append(dst, statusFieldLen)
	return strconv.AppendInt(dst, int64(status), decimal)
}

// appendContentLength appends a content-length field.
func appendContentLength(dst []byte, n int) []byte {
	dst = appendIndexedLiteralName(dst, contentLengthIndex)
	lengthAt := len(dst)
	dst = append(dst, 0)
	dst = strconv.AppendInt(dst, int64(n), decimal)
	dst[lengthAt] = byte(len(dst) - lengthAt - 1)
	return dst
}

// appendIndexedLiteralName opens a never-indexed literal whose name is at
// index in the static table.
func appendIndexedLiteralName(dst []byte, index int) []byte {
	//: the 4-bit integer holds the index, or the prefix and its overflow.
	if index < prefix4Max {
		return append(dst, neverIndexedByte|byte(index))
	}
	return append(dst, neverIndexedByte|byte(prefix4Max), byte(index-prefix4Max))
}

// appendExtraFields appends the fields http1's writer adds after Header: the
// CORS lines, Retry-After and X-Request-Id.
func appendExtraFields(dst []byte, res *corenet.HTTP1ResponseValue) ([]byte, bool) {
	dst, ok := appendResponseFields(dst, res.CORS)
	//: a malformed CORS line, as a malformed Header line is.
	if !ok {
		return dst, false
	}
	var line [extraFieldMax]byte
	//: a refusal's delay.
	if res.RetryAfter > 0 {
		out := strconv.AppendInt(append(line[:0], "Retry-After: "...), int64(res.RetryAfter), 10)
		dst, ok = appendField(dst, out)
		//: the line is the adapter's own, so this does not refuse.
		if !ok {
			return dst, false
		}
	}
	//: the identifier a request-id link answers with.
	if id := res.RequestID.Bytes(); len(id) > 0 && len(id) < extraFieldMax-len("X-Request-Id: ") {
		dst, ok = appendField(dst, append(append(line[:0], "X-Request-Id: "...), id...))
	}
	return dst, ok
}

// appendResponseFields re-reads res.Header's lines as HPACK literals, false on
// a malformed line.
func appendResponseFields(dst, header []byte) ([]byte, bool) {
	//: each line is "Name: value\r\n", written by AppendField.
	for len(header) > 0 {
		end := bytes.IndexByte(header, asciiLF)
		//: a line with no end is not one AppendField writes.
		if end < 1 || header[end-1] != asciiCR {
			return dst, false
		}
		var ok bool
		//: a field the block cannot carry ends the response's headers.
		if dst, ok = appendField(dst, header[:end-1]); !ok {
			return dst, false
		}
		header = header[end+1:]
	}
	return dst, true
}

// appendField appends one "Name: value" line as a literal field.
func appendField(dst, line []byte) ([]byte, bool) {
	colon := bytes.IndexByte(line, asciiColonByte)
	//: a line needs a name and its colon.
	if colon <= 0 || colon >= maxNameLen || !isToken(line[:colon]) {
		return dst, false
	}
	name, value := line[:colon], bytes.Trim(line[colon+1:], " \t")
	//: the value of a header line holds no NUL, CR or LF.
	if valueStop.Index(value) >= 0 {
		return dst, false
	}
	//: a connection-specific field is dropped, never spelled in HTTP/2.
	if isConnectionField(name) {
		return dst, true
	}
	return appendNamedField(dst, name, value), true
}

// isToken reports whether name is an RFC 9110 token.
func isToken(name []byte) bool {
	//: every byte, the bit set says.
	for _, c := range name {
		//: the first byte outside the set settles it.
		if tokenBits[c>>6]&(1<<(c&63)) == 0 {
			return false
		}
	}
	return true
}

// isConnectionField reports whether name is one of the fields HTTP/2 does not
// carry; the comparison folds, the line's case being a handler's.
func isConnectionField(name []byte) bool {
	//: a linear walk: six short names.
	for _, c := range connectionFields {
		//: the first match settles it.
		if equalFold(name, c) {
			return true
		}
	}
	return false
}

// appendNamedField appends a literal field: the name by static index when the
// table has it, otherwise lowercased as a string; the value as a plain string.
func appendNamedField(dst, name, value []byte) []byte {
	//: the name is the static table's: only its index is written.
	if index := staticNameIndex(name); index > 0 {
		dst = appendIndexedLiteralName(dst, index)
		return appendString(dst, value)
	}
	dst = append(dst, neverIndexedByte)
	dst = appendIntOfLen(dst, len(name))
	at := len(dst)
	dst = append(dst, name...)
	foldToLower(dst[at:])
	return appendString(dst, value)
}

// equalFold reports whether name and s are equal once ASCII capitals fold.
func equalFold(name []byte, s string) bool {
	//: a length mismatch settles it before any byte is read.
	if len(name) != len(s) {
		return false
	}
	//: byte by byte; only A through Z fold.
	for i := range len(name) {
		//: one differing byte.
		if lowerByte(name[i]) != s[i] {
			return false
		}
	}
	return true
}

// lowerByte folds an ASCII capital to lowercase.
func lowerByte(c byte) byte {
	//: only 'A' through 'Z' move.
	if c >= firstUpper && c <= lastUpper {
		return c | toLower
	}
	return c
}

// appendString appends a plain string, RFC 7541 §5.2: the length as a 7-bit
// integer, no Huffman coding.
func appendString(dst, s []byte) []byte {
	dst = appendIntOfLen(dst, len(s))
	return append(dst, s...)
}

// appendIntOfLen appends a string length as a 7-bit integer with the
// Huffman bit clear, RFC 7541 §5.1.
func appendIntOfLen(dst []byte, n int) []byte {
	//: a length that fits the prefix takes one byte.
	if n < stringLenThreshold {
		return append(dst, byte(n))
	}
	dst = append(dst, byte(stringLenThreshold))
	n -= stringLenThreshold
	//: seven bits at a time, the low ones first.
	for n >= int(continuationBit) {
		dst = append(dst, byte(n%int(continuationBit))|continuationBit)
		n /= int(continuationBit)
	}
	return append(dst, byte(n))
}

// foldToLower lowercases ASCII capitals in place.
func foldToLower(b []byte) {
	//: only capitals move.
	for i, c := range b {
		b[i] = lowerByte(c)
	}
}

// staticNameIndex returns the static table's lowest index for name, compared
// without regard to ASCII case, zero when it has none.
func staticNameIndex(name []byte) int {
	//: the table is grouped by name length: a longer name has no entry.
	if len(name) > staticNameMax {
		return 0
	}
	spanAt := 2 * len(name)
	//: names of this length are few.
	for _, index := range staticSorted[staticSpans[spanAt]:staticSpans[spanAt+1]] {
		at := staticOffsetSlots * (int(index) - 1)
		off, length := int(staticOffsets[at]), int(staticOffsets[at+1])
		//: the first entry of a matching name is its lowest index.
		if length == len(name) && equalFoldBytes(name, staticBytes[off:off+length]) {
			return int(index)
		}
	}
	return 0
}

// equalFoldBytes reports whether name equals the lowercase table name b.
func equalFoldBytes(name, b []byte) bool {
	//: a static name is ASCII lowercase, so only name folds.
	for i := range len(name) {
		//: one differing byte.
		if lowerByte(name[i]) != b[i] {
			return false
		}
	}
	return true
}

// frameBlock writes the header of the frame the block occupies and, when the
// block is larger than maxFrame, splits it over CONTINUATION frames. Bytes are
// moved within dst, so it allocates only when dst must grow.
func (e Encoder) frameBlock(dst []byte, start, blockStart int, stream uint32, endStream bool) []byte {
	blockLen := len(dst) - blockStart
	maxFrame := max(e.maxFrame, corehttp2.DefaultMaxFrame)
	flags := corehttp2.FlagEndHeaders
	//: the stream ends on this block when the caller has nothing else to send.
	if endStream {
		flags |= corehttp2.FlagEndStream
	}
	//: the whole block fits one frame: nothing to shift.
	if blockLen <= maxFrame {
		h := corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: flags, StreamID: stream, Length: blockLen}
		AppendFrameHeader(dst[:start], h)
		return dst
	}
	return e.splitBlock(dst, start, blockStart, stream, flags)
}

// splitBlock frames a block over one HEADERS and as many CONTINUATION frames
// as it takes, writing from the end so nothing is copied twice.
func (e Encoder) splitBlock(dst []byte, start, blockStart int, stream uint32, flags corehttp2.FrameFlags) []byte {
	maxFrame := max(e.maxFrame, corehttp2.DefaultMaxFrame)
	blockLen := len(dst) - blockStart
	extra := (blockLen - 1) / maxFrame
	end := len(dst) + extra*corehttp2.FrameHeaderLen
	dst = append(dst, make([]byte, extra*corehttp2.FrameHeaderLen)...)
	writeTail := end
	//: from the last fragment to the second; a fragment is at most maxFrame.
	for i := extra; i > 0; i-- {
		fragment := blockLen - i*maxFrame
		fragStart := blockStart + i*maxFrame
		fragEnd := fragStart + fragment
		copy(dst[writeTail-fragment:writeTail], dst[fragStart:fragEnd])
		h := corehttp2.FrameValue{Type: corehttp2.FrameContinuation, StreamID: stream, Length: fragment}
		//: only the last CONTINUATION ends the block.
		if i == extra {
			h.Flags = corehttp2.FlagEndHeaders
		}
		AppendFrameHeader(dst[:writeTail-fragment-corehttp2.FrameHeaderLen], h)
		writeTail -= fragment + corehttp2.FrameHeaderLen
		blockLen = fragStart - blockStart
	}
	//: the HEADERS frame carries the first fragment; END_HEADERS lives on the last CONTINUATION.
	h := corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: flags &^ corehttp2.FlagEndHeaders, StreamID: stream, Length: maxFrame}
	AppendFrameHeader(dst[:start], h)
	return dst
}
