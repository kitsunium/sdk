// Package tlv — reflection-driven decoder shared by Unmarshal and the
// streaming Decoder. Every helper returns the residual byte slice plus a
// wrapped *errs.Error on failure.
package tlv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tlvDecoder adapts an io.Reader to codec.Decoder. Each Decode call
// consumes exactly one independent TLV record from the reader.
type tlvDecoder struct {
	r io.Reader
	//: sticky EOF latch so More() returns false once the stream drains.
	done bool
}

// Decode reads one TLV record from the wrapped reader and unmarshals it
// into v (which must be a non-nil pointer).
func (d *tlvDecoder) Decode(v any) error {
	//: read one full record into a scratch buffer.
	buf, rerr := readOneRecord(d.r)
	//: EOF latches More() to false and propagates the stdlib sentinel.
	if errors.Is(rerr, io.EOF) {
		//: drained; remember it.
		d.done = true
		//: pass io.EOF through untouched per the contract.
		return io.EOF
	}
	//: I/O failures bail out wrapped.
	if rerr != nil {
		//: any non-EOF read failure halts the stream.
		d.done = true
		//: surface the wrapped sentinel.
		return rerr
	}
	//: dispatch through the buffer decode.
	return decodeRoot(buf, v)
}

// More reports whether additional records remain on the stream.
func (d *tlvDecoder) More() bool {
	//: reflect the sticky EOF latch.
	return !d.done
}

// readOneRecord reads exactly one independent TLV record from r. A clean
// io.EOF before the very first tag byte means the stream has drained and is
// propagated verbatim; any other read failure (including EOF mid-record) is
// wrapped as TRUNCATED / UNMARSHAL_FAILED. V59: composite records (slice /
// map / struct) carry an element/pair/field COUNT in their length header,
// not a byte length, so the body must be parsed by recursing into the
// declared number of sub-records rather than copying length value bytes.
func readOneRecord(r io.Reader) (record []byte, err error) {
	//: tag byte first; EOF here is a clean stream end.
	tag, terr := readTag(r)
	//: stream end — propagate io.EOF verbatim.
	if errors.Is(terr, io.EOF) {
		//: clean shutdown.
		return nil, io.EOF
	}
	//: anything else is a hard failure.
	if terr != nil {
		//: wrap as an unmarshal failure.
		return nil, terr
	}
	//: tag in hand; read the rest of this record (header + body) at depth 0.
	return readRecordBody(r, tag, 0)
}

// readRecordBody reads the LEB128 length header for an already-consumed tag
// and the record body, returning the reassembled tag+length+value bytes.
// depth bounds composite recursion against CWE-674 stack exhaustion.
func readRecordBody(r io.Reader, tag byte, depth int) (record []byte, err error) {
	//: nesting guard — mirror the buffered decoder's maxTLVDepth cap.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return nil, depthExceededError(depth)
	}
	//: read the LEB128 length one byte at a time.
	length, lerr := readUvarint(r)
	//: surface any varint failure.
	if lerr != nil {
		//: already wrapped.
		return nil, lerr
	}
	//: bound the declared length before allocating or recursing.
	if length > uint64(maxTLVBytes) {
		//: surface the documented size sentinel.
		return nil, sizeExceededError(length)
	}
	//: composite tags carry a sub-record COUNT; scalars carry a byte length.
	if isCompositeTag(Tag(tag)) {
		//: recurse into the declared number of sub-records.
		return readCompositeRecord(r, tag, length, depth)
	}
	//: scalar body is exactly length value bytes.
	return assembleRecord(r, tag, length)
}

// isCompositeTag reports whether tag's length header is an element / pair /
// field COUNT (slice, map, struct) rather than a byte length.
func isCompositeTag(tag Tag) bool {
	//: slice / map / struct are the only count-prefixed families.
	return tag == tagSlice || tag == tagMap || tag == tagStruct
}

// subRecordCount returns how many child TLV records follow a composite
// header: one per slice element, two per map pair (key+value), two per
// struct field (name+value). length is the declared family count.
func subRecordCount(tag Tag, length uint64) uint64 {
	//: maps and structs frame each entry as two adjacent records.
	if tag == tagMap || tag == tagStruct {
		//: key+value or name+value per entry.
		return length * recordsPerPair
	}
	//: slices frame one record per element.
	return length
}

// readCompositeRecord reassembles a composite record (slice / map / struct)
// by reading its declared sub-records recursively, so the streaming reader
// frames the body identically to the buffered decoder (V59). The rebuilt
// bytes are tag + length-as-count + the concatenated child records, which
// decodeRoot then parses exactly as the buffered path does.
func readCompositeRecord(r io.Reader, tag byte, length uint64, depth int) (record []byte, err error) {
	//: header carries the family count verbatim — preserve the wire shape.
	out := make([]byte, 0, 1+maxVarintBytes)
	out = append(out, tag)
	out = binary.AppendUvarint(out, length)
	//: each composite frames a fixed number of child records.
	for range subRecordCount(Tag(tag), length) {
		//: a child record opens with its own tag byte.
		childTag, terr := readTag(r)
		//: a mid-composite EOF is a truncated record, never a clean end.
		if errors.Is(terr, io.EOF) {
			//: surface as TRUNCATED.
			return nil, truncatedError()
		}
		//: any other read failure is wrapped already.
		if terr != nil {
			//: surface verbatim.
			return nil, terr
		}
		//: recurse one level deeper to read the child body.
		child, cerr := readRecordBody(r, childTag, depth+1)
		//: surface any child failure verbatim.
		if cerr != nil {
			//: already wrapped.
			return nil, cerr
		}
		//: append the child record bytes.
		out = append(out, child...)
	}
	//: caller owns the reassembled composite record.
	return out, nil
}

// readTag reads exactly one byte (the TLV tag) from r and returns
// io.EOF unchanged when the stream is empty.
func readTag(r io.Reader) (tag byte, err error) {
	//: single-byte buffer reused per call.
	var buf [1]byte
	//: ReadFull turns a partial read into io.ErrUnexpectedEOF.
	n, rerr := io.ReadFull(r, buf[:])
	//: stream end — propagate io.EOF verbatim.
	if errors.Is(rerr, io.EOF) && n == 0 {
		//: clean shutdown.
		return 0, io.EOF
	}
	//: any other read failure is wrapped.
	if rerr != nil {
		//: classify by error kind (EOF mid-record → TRUNCATED).
		return 0, wrapReadFailure(rerr)
	}
	//: hand back the byte read.
	return buf[0], nil
}

// assembleRecord reads length value-bytes from r and rebuilds the
// original tag+length+value record byte sequence. The value buffer grows
// in chunks so a crafted header declaring length≈maxTLVBytes against a
// reader that delivers only a handful of bytes pays only the bytes
// actually delivered, not the declared cap (CWE-400 defence).
func assembleRecord(r io.Reader, tag byte, length uint64) (record []byte, err error) {
	//: read the value bytes through a LimitReader so the read budget cannot
	//: exceed the declared length, then drain incrementally so the buffer
	//: only grows to the bytes the reader actually delivers.
	limited := &io.LimitedReader{R: r, N: int64(length)} //nolint:gosec // length ≤ maxTLVBytes
	//: streamReadBuffer grows naturally; ReadFrom uses 512-byte chunks.
	var value bytes.Buffer
	//: drain the limit-reader; short read surfaces as io.ErrUnexpectedEOF.
	if _, verr := value.ReadFrom(limited); verr != nil {
		//: surface as TRUNCATED via the shared wrapper.
		return nil, wrapReadFailure(verr)
	}
	//: detect declared-but-undelivered bytes.
	if uint64(value.Len()) != length {
		//: short read maps to TRUNCATED.
		return nil, wrapReadFailure(io.ErrUnexpectedEOF)
	}
	//: rebuild the original record: tag + length + value.
	out := make([]byte, 0, 1+maxVarintBytes+value.Len())
	out = append(out, tag)
	out = binary.AppendUvarint(out, length)
	out = append(out, value.Bytes()...)
	//: caller now owns the record bytes.
	return out, nil
}

// readUvarint reads a single LEB128 length from r one byte at a time so
// the buffer stays exactly large enough.
func readUvarint(r io.Reader) (value uint64, err error) {
	//: 10-byte budget covers any uint64.
	var shift uint
	//: one byte at a time; LEB128 is little-endian 7-bit groups.
	for i := range maxVarintBytes {
		//: read one byte.
		var buf [1]byte
		//: short read = truncated.
		if _, rerr := io.ReadFull(r, buf[:]); rerr != nil {
			//: wrap and surface.
			return 0, wrapReadFailure(rerr)
		}
		//: continuation bit is high; lower 7 bits are payload.
		b := buf[0]
		//: terminal byte — finish without setting continuation.
		if b < varintContinuationBit {
			//: detect overflow on the most-significant byte.
			if i == maxVarintBytes-1 && b > varintMaxLastByte {
				//: surface as malformed.
				return 0, varintOverflowError()
			}
			//: accumulate the final 7 bits and return.
			return value | uint64(b)<<shift, nil
		}
		//: accumulate the low 7 bits and advance.
		value |= uint64(b&varintPayloadMask) << shift
		shift += varintShiftStep
	}
	//: exceeded the 10-byte budget — malformed.
	return 0, varintOverflowError()
}

// wrapReadFailure turns an io.ReadFull error into the right TLV sentinel.
func wrapReadFailure(rerr error) error {
	//: short read maps to TRUNCATED; everything else to UNMARSHAL_FAILED.
	if errors.Is(rerr, io.ErrUnexpectedEOF) || errors.Is(rerr, io.EOF) {
		//: surface the truncated sentinel.
		return errs.Wrap(rerr, errs.WrapParams{
			Code:    CodeTLVTruncated,
			Reason:  "TRUNCATED",
			Public:  "TLV buffer truncated",
			Private: "service/codec/tlv: reader exhausted before record completed",
		})
	}
	//: generic decode failure.
	return errs.Wrap(rerr, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv: reader returned an error",
	})
}

// decodeRoot validates the pointer-shape contract and decodes a single
// TLV record from data into the pointee.
func decodeRoot(data []byte, v any) error {
	//: target must be a non-nil pointer.
	rv := reflect.ValueOf(v)
	//: shape rejection.
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		//: surface a value-invalid sentinel.
		return nonPointerTargetError()
	}
	//: target is the pointee; settability is required for Set to work.
	target := rv.Elem()
	//: target-aware fast path: if the target is a typed struct AND the
	//: wire bytes carry a struct record, decode straight into the
	//: struct fields (skips the map[string]any intermediate AND the
	//: subsequent projectMapToStruct second walk). Returns handled=true
	//: when it took the typed path; the fallback below covers everything
	//: else (interface targets, slices, maps, scalars).
	if handled, err := tryDecodeRootInto(data, target); handled {
		//: typed path consumed the full buffer; surface err verbatim.
		return err
	}
	//: decode one record at depth zero.
	value, rest, derr := decodeValue(data, 0)
	//: surface decode failure verbatim.
	if derr != nil {
		//: already wrapped.
		return derr
	}
	//: reject trailing bytes — Unmarshal expects exactly one record.
	if len(rest) != 0 {
		//: surface as decode failure for callers that match by reason.
		return trailingBytesError(len(rest))
	}
	//: wrap the target in a local view so the helper signature avoids
	//: an externally-named concrete type.
	return assignDecoded(reflectView(target), target.Type(), value)
}

// decodeValue reads one TLV record at data[0:] and returns the decoded
// Go value plus the remaining bytes.
func decodeValue(data []byte, depth int) (value any, rest []byte, err error) {
	//: nesting guard.
	if depth > maxTLVDepth {
		//: surface the documented sentinel.
		return nil, data, depthExceededError(depth)
	}
	//: every record needs at least 2 bytes (tag + 1-byte length).
	if len(data) < minRecordBytes {
		//: surface the truncated sentinel.
		return nil, data, truncatedError()
	}
	//: parse the tag.
	tag := Tag(data[0])
	rest = data[1:]
	//: read the LEB128 length.
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure.
	if lerr != nil {
		//: already wrapped.
		return nil, data, lerr
	}
	//: bound the per-record value length before further decoding.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return nil, data, sizeExceededError(length)
	}
	//: dispatch on the tag.
	return decodeByTag(tag, length, rest, depth)
}

// decodeByTag dispatches on the tag's family band. The high nibble
// distinguishes scalar primitives, slices, maps, and structs so the
// dispatch table stays narrow.
func decodeByTag(tag Tag, length uint64, rest []byte, depth int) (value any, residual []byte, err error) {
	//: scalars share the 0x00-0x4F band.
	if tag <= tagBytes {
		//: route to the scalar dispatcher.
		return decodeScalarByTag(tag, length, rest)
	}
	//: composite branches — slice / map / struct.
	switch tag {
	//: heterogeneous slice (length = element count).
	case tagSlice:
		//: helper iterates element by element.
		return decodeSlice(length, rest, depth)
	//: map (length = pair count).
	case tagMap:
		//: helper iterates key/value pairs.
		return decodeMap(length, rest, depth)
	//: struct (length = field count).
	case tagStruct:
		//: helper iterates field records.
		return decodeStruct(length, rest, depth)
	//: unknown tag — surface as decode failure.
	default:
		//: include the byte for diagnostics.
		return nil, rest, unknownTagError(tag)
	}
}

// decodeScalarByTag handles every fixed-payload tag (nil, bool, ints,
// uints, floats, string, bytes). Splits into zero-payload, numeric, and
// length-prefixed dispatch sub-helpers so the cyclomatic complexity of
// each function stays below the linter's budget.
func decodeScalarByTag(tag Tag, length uint64, rest []byte) (value any, residual []byte, err error) {
	//: zero-payload tags (nil, false, true) handled first.
	if zeroVal, zeroRest, handled := tryZeroPayloadTag(tag, rest); handled {
		//: zero-payload tags fail only on bad length.
		if length != 0 {
			//: surface as malformed length.
			return nil, rest, malformedLengthError(0, length)
		}
		//: success.
		return zeroVal, zeroRest, nil
	}
	//: numeric tags (int*, uint*, float*) handled next.
	if numVal, numRest, handled, numErr := tryNumericTag(tag, length, rest); handled {
		//: dispatched.
		return numVal, numRest, numErr
	}
	//: length-prefixed scalar tags (string, bytes).
	switch tag {
	//: UTF-8 string — length = byte count.
	case tagString:
		//: route to the string helper.
		return decodeString(length, rest)
	//: opaque bytes — length = byte count.
	case tagBytes:
		//: route to the bytes helper.
		return decodeBytes(length, rest)
	//: tag fell into the scalar band but matched no entry — malformed.
	default:
		//: include the byte for diagnostics.
		return nil, rest, unknownTagError(tag)
	}
}

// tryZeroPayloadTag handles tagNil/BoolFalse/BoolTrue. Emits the
// constant value when the tag matches; the caller enforces the length-0
// invariant before consuming the residual buffer.
func tryZeroPayloadTag(tag Tag, rest []byte) (value any, residual []byte, handled bool) {
	//: dispatch on the zero-payload band.
	switch tag {
	//: typed nil.
	case tagNil:
		//: untyped nil constant.
		return nil, rest, true
	//: boolean false.
	case tagBoolFalse:
		//: false constant.
		return false, rest, true
	//: boolean true.
	case tagBoolTrue:
		//: true constant.
		return true, rest, true
	//: tag not in this band.
	default:
		//: caller fans out further.
		return nil, rest, false
	}
}

// tryNumericTag handles every signed/unsigned/float tag. Returns
// handled=false when tag falls outside the numeric bands. err is the
// last named return so the lint rule does not flag it.
func tryNumericTag(tag Tag, length uint64, rest []byte) (value any, residual []byte, handled bool, err error) {
	//: signed integers.
	switch tag {
	//: int*.
	case tagInt8, tagInt16, tagInt32, tagInt64:
		//: width selection by tag.
		intVal, intRest, intErr := decodeInt(tag, length, rest)
		//: numeric dispatched; bubble the result with handled=true.
		return intVal, intRest, true, intErr
	//: uint*.
	case tagUint8, tagUint16, tagUint32, tagUint64:
		//: width selection by tag.
		uintVal, uintRest, uintErr := decodeUint(tag, length, rest)
		//: numeric dispatched; bubble the result with handled=true.
		return uintVal, uintRest, true, uintErr
	//: float*.
	case tagFloat32, tagFloat64:
		//: width selection by tag.
		fVal, fRest, fErr := decodeFloat(tag, length, rest)
		//: numeric dispatched; bubble the result with handled=true.
		return fVal, fRest, true, fErr
	//: tag not numeric.
	default:
		//: caller falls back to the next band.
		return nil, rest, false, nil
	}
}

// readVarintFromBytes reads one LEB128 value from the head of data.
// 1-byte LEB128 (data[0] < 0x80) is the common case — inline that check
// before calling binary.Uvarint to skip the function-call frame and the
// loop setup on the > 90% common path (audit TL7).
func readVarintFromBytes(data []byte) (value uint64, rest []byte, err error) {
	//: 1-byte LEB128 fast-path: continuation bit clear means we have
	//: the whole length in data[0] already.
	if len(data) > 0 && uint64(data[0]) < uvarintSingleByteCap {
		//: tag-and-length stride is 1 byte; return rest with that
		//: advance and the length in the low 7 bits.
		return uint64(data[0]), data[1:], nil
	}
	//: delegate to the stdlib helper.
	val, n := binary.Uvarint(data)
	//: n == 0 means buffer too small.
	if n == 0 {
		//: short buffer.
		return 0, data, truncatedError()
	}
	//: negative n indicates overflow.
	if n < 0 {
		//: surface as malformed varint.
		return 0, data, varintOverflowError()
	}
	//: success.
	return val, data[n:], nil
}

// decodeInt reads a signed integer of the width implied by tag.
//
//nolint:gocyclo // tag-driven dispatch.
func decodeInt(tag Tag, length uint64, rest []byte) (value any, residual []byte, err error) {
	//: width lookup driven by the tag itself.
	w := intWidth(tag)
	//: enforce the expected payload length.
	if length != uint64(w) {
		//: malformed record.
		return nil, rest, malformedLengthError(uint64(w), length)
	}
	//: bound the buffer.
	if uint64(len(rest)) < length {
		//: short read.
		return nil, rest, truncatedError()
	}
	//: tag-driven decode.
	switch tag {
	//: 8-bit.
	case tagInt8:
		//: sign-extend through int8.
		return int64(int8(rest[0])), rest[1:], nil
	//: 16-bit.
	case tagInt16:
		//: big-endian 2-byte signed.
		return int64(int16(binary.BigEndian.Uint16(rest[:width16]))), rest[width16:], nil //nolint:gosec // wire reinterpret
	//: 32-bit.
	case tagInt32:
		//: big-endian 4-byte signed.
		return int64(int32(binary.BigEndian.Uint32(rest[:width32]))), rest[width32:], nil //nolint:gosec // wire reinterpret
	//: 64-bit.
	case tagInt64:
		//: big-endian 8-byte signed.
		return int64(binary.BigEndian.Uint64(rest[:width64])), rest[width64:], nil //nolint:gosec // wire reinterpret
	//: unreachable — caller vetted tag.
	default:
		//: defensive return.
		return nil, rest, unknownTagError(tag)
	}
}

// decodeUint reads an unsigned integer of the width implied by tag.
//
//nolint:gocyclo // tag-driven dispatch.
func decodeUint(tag Tag, length uint64, rest []byte) (value any, residual []byte, err error) {
	//: width lookup driven by the tag itself.
	w := uintWidth(tag)
	//: enforce the expected payload length.
	if length != uint64(w) {
		//: malformed record.
		return nil, rest, malformedLengthError(uint64(w), length)
	}
	//: bound the buffer.
	if uint64(len(rest)) < length {
		//: short read.
		return nil, rest, truncatedError()
	}
	//: tag-driven decode.
	switch tag {
	//: 8-bit.
	case tagUint8:
		//: single byte.
		return uint64(rest[0]), rest[1:], nil
	//: 16-bit.
	case tagUint16:
		//: big-endian 2-byte unsigned.
		return uint64(binary.BigEndian.Uint16(rest[:width16])), rest[width16:], nil
	//: 32-bit.
	case tagUint32:
		//: big-endian 4-byte unsigned.
		return uint64(binary.BigEndian.Uint32(rest[:width32])), rest[width32:], nil
	//: 64-bit.
	case tagUint64:
		//: big-endian 8-byte unsigned.
		return binary.BigEndian.Uint64(rest[:width64]), rest[width64:], nil
	//: unreachable — caller vetted tag.
	default:
		//: defensive return.
		return nil, rest, unknownTagError(tag)
	}
}

// decodeFloat reads an IEEE-754 float of the width implied by tag.
// Splits the malformed-length and truncated-buffer paths so callers can
// distinguish a header lying about the width from a payload running out
// of bytes (mirrors decodeInt / decodeUint).
func decodeFloat(tag Tag, length uint64, rest []byte) (value any, residual []byte, err error) {
	//: 32-bit branch.
	if tag == tagFloat32 {
		//: declared length must match the tag width.
		if length != uint64(width32) {
			//: surface as malformed length.
			return nil, rest, malformedLengthError(uint64(width32), length)
		}
		//: buffer must hold the declared payload.
		if uint64(len(rest)) < uint64(width32) {
			//: surface as truncated (mirrors decodeInt / decodeUint).
			return nil, rest, truncatedError()
		}
		//: decode the single.
		return float64(math.Float32frombits(binary.BigEndian.Uint32(rest[:width32]))), rest[width32:], nil
	}
	//: 64-bit branch — declared length must match.
	if length != uint64(width64) {
		//: surface as malformed length.
		return nil, rest, malformedLengthError(uint64(width64), length)
	}
	//: 64-bit branch — buffer must hold the declared payload.
	if uint64(len(rest)) < uint64(width64) {
		//: surface as truncated (mirrors decodeInt / decodeUint).
		return nil, rest, truncatedError()
	}
	//: decode the double.
	return math.Float64frombits(binary.BigEndian.Uint64(rest[:width64])), rest[width64:], nil
}

// decodeString reads a UTF-8 string. length is the byte count.
func decodeString(length uint64, rest []byte) (value any, residual []byte, err error) {
	//: bound the buffer.
	if uint64(len(rest)) < length {
		//: short read.
		return nil, rest, truncatedError()
	}
	//: copy the bytes into a fresh string to avoid aliasing the input.
	return string(rest[:length]), rest[length:], nil
}

// decodeBytes reads an opaque byte sequence. length is the byte count.
func decodeBytes(length uint64, rest []byte) (value any, residual []byte, err error) {
	//: bound the buffer.
	if uint64(len(rest)) < length {
		//: short read.
		return nil, rest, truncatedError()
	}
	//: copy the bytes into a fresh slice to avoid aliasing the input.
	out := make([]byte, length)
	copy(out, rest[:length])
	//: advance the cursor.
	return out, rest[length:], nil
}

// decodeSlice reads a tagSlice record: length is the element count.
// The initial capacity is clamped by sliceHint so a crafted header
// declaring a huge count cannot trigger a multi-GB pre-allocation
// before the elements are actually read (CWE-400 defence). The slice
// still grows on demand as each element decodes, so the final size
// matches the actual element stream.
func decodeSlice(length uint64, rest []byte, depth int) (value any, residual []byte, err error) {
	//: collect every element into a freshly-allocated []any with a clamped
	//: initial capacity — append() grows the slice as elements arrive.
	out := make([]any, 0, sliceHint(length))
	//: each element is a full TLV record.
	for range length {
		//: decode the next element.
		val, next, derr := decodeValue(rest, depth+1)
		//: surface any per-element failure.
		if derr != nil {
			//: already wrapped.
			return nil, rest, derr
		}
		//: collect and advance.
		out = append(out, val)
		rest = next
	}
	//: success.
	return out, rest, nil
}

// decodeMap reads a tagMap record: length is the pair count.
// The initial bucket hint is clamped by sliceHint so a crafted header
// declaring a huge pair count cannot trigger a multi-GB pre-allocation
// before the pairs are actually read (CWE-400 defence). The map still
// grows on demand as each pair decodes.
func decodeMap(length uint64, rest []byte, depth int) (value any, residual []byte, err error) {
	//: collect into map[any]any with a clamped initial bucket hint; the
	//: map grows on demand as each pair arrives.
	out := make(map[any]any, sliceHint(length))
	//: each pair is two adjacent TLV records.
	for range length {
		//: decode the key.
		key, next, kerr := decodeValue(rest, depth+1)
		//: surface key failure.
		if kerr != nil {
			//: already wrapped.
			return nil, rest, kerr
		}
		//: decode the value into the residual buffer.
		val, next2, verr := decodeValue(next, depth+1)
		//: surface value failure.
		if verr != nil {
			//: already wrapped.
			return nil, rest, verr
		}
		//: map keys must be hashable.
		if key != nil && !reflect.TypeOf(key).Comparable() {
			//: unhashable key surfaces as decode failure.
			return nil, rest, unhashableKeyError()
		}
		//: install the pair.
		out[key] = val
		//: advance past the pair.
		rest = next2
	}
	//: success.
	return out, rest, nil
}

// decodeStruct reads a tagStruct record: length is the field count.
// The Go representation is map[string]any since the wire is self-describing.
// The initial bucket hint is clamped by sliceHint so a crafted header
// declaring a huge field count cannot trigger a multi-GB pre-allocation
// before the fields are actually read (CWE-400 defence). The map still
// grows on demand as each field decodes.
func decodeStruct(length uint64, rest []byte, depth int) (value any, residual []byte, err error) {
	//: collect into map[string]any with a clamped initial bucket hint;
	//: the map grows on demand as each field arrives.
	out := make(map[string]any, sliceHint(length))
	//: each entry is name-TLV (string) then value-TLV.
	for range length {
		//: peek the name record header to enforce the maxFieldNameBytes
		//: cap BEFORE the generic decoder would allocate the name buffer.
		nameStr, next, nerr := decodeFieldName(rest, depth+1)
		//: surface name failure.
		if nerr != nil {
			//: already wrapped.
			return nil, rest, nerr
		}
		//: decode the value.
		val, next2, verr := decodeValue(next, depth+1)
		//: surface value failure.
		if verr != nil {
			//: already wrapped.
			return nil, rest, verr
		}
		//: install the field.
		out[nameStr] = val
		//: advance past the field record pair.
		rest = next2
	}
	//: success.
	return out, rest, nil
}

// decodeFieldName reads the name-TLV at the head of data, mirroring the
// encoder's maxFieldNameBytes cap (255 bytes). A crafted payload that
// declares a huge field-name length is rejected before any allocation —
// this is a defence-in-depth check; decodeString's general "length ≤
// len(rest)" guard would also catch the make([]byte, length) attempt,
// but the explicit cap gives callers a precise diagnostic.
func decodeFieldName(data []byte, depth int) (name string, rest []byte, err error) {
	//: nesting guard — names live one level below the struct.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return "", data, depthExceededError(depth)
	}
	//: every record needs at least 2 bytes (tag + 1-byte length).
	if len(data) < minRecordBytes {
		//: surface the truncated sentinel.
		return "", data, truncatedError()
	}
	//: the only legal tag here is tagString.
	tag := Tag(data[0])
	//: malformed when the name is not a string TLV.
	if tag != tagString {
		//: surface as decode failure with the offending tag.
		return "", data, nonStringFieldNameError()
	}
	//: read the LEB128 length.
	length, after, lerr := readVarintFromBytes(data[1:])
	//: surface varint failure.
	if lerr != nil {
		//: already wrapped.
		return "", data, lerr
	}
	//: enforce the encoder-side cap before allocating the name buffer.
	if length > uint64(maxFieldNameBytes) {
		//: surface as decode failure with a precise diagnostic.
		return "", data, fieldNameTooLongDecodeError(length)
	}
	//: decode the string body via the shared helper.
	val, residual, derr := decodeString(length, after)
	//: surface decode failure.
	if derr != nil {
		//: already wrapped.
		return "", data, derr
	}
	//: decodeString returns a string in the any.
	nameStr, ok := val.(string)
	//: malformed when decodeString returned a non-string (defensive).
	if !ok {
		//: surface as decode failure.
		return "", data, nonStringFieldNameError()
	}
	//: success.
	return nameStr, residual, nil
}

// intWidth returns the byte width of a signed-integer tag.
func intWidth(tag Tag) int {
	//: lookup table inline avoids a map allocation.
	switch tag {
	//: 8-bit.
	case tagInt8:
		//: 1 byte.
		return width8
	//: 16-bit.
	case tagInt16:
		//: 2 bytes.
		return width16
	//: 32-bit.
	case tagInt32:
		//: 4 bytes.
		return width32
	//: 64-bit.
	case tagInt64:
		//: 8 bytes.
		return width64
	//: unknown — caller already vetted the tag, but be defensive.
	default:
		//: zero signals an unknown tag.
		return 0
	}
}

// uintWidth returns the byte width of an unsigned-integer tag.
func uintWidth(tag Tag) int {
	//: lookup table inline avoids a map allocation.
	switch tag {
	//: 8-bit.
	case tagUint8:
		//: 1 byte.
		return width8
	//: 16-bit.
	case tagUint16:
		//: 2 bytes.
		return width16
	//: 32-bit.
	case tagUint32:
		//: 4 bytes.
		return width32
	//: 64-bit.
	case tagUint64:
		//: 8 bytes.
		return width64
	//: unknown — caller already vetted the tag, but be defensive.
	default:
		//: zero signals an unknown tag.
		return 0
	}
}

// sliceHint clamps an attacker-declared length to sliceHintCap so a
// malformed buffer cannot trigger a multi-million-entry pre-allocation.
func sliceHint(length uint64) int {
	//: never pre-allocate more than the cap.
	if length > uint64(sliceHintCap) {
		//: clamp.
		return sliceHintCap
	}
	//: small enough to use as-is.
	return int(length) //nolint:gosec // clamped above
}

// assignDecoded publishes value into the pre-extracted target pointee.
// targetType is target.Type(); both are passed to avoid re-querying.
// target is a reflectView wrapper to keep the signature free of an
// externally-named concrete type.
func assignDecoded(target reflectView, targetType reflect.Type, value any) error {
	//: convert once for method dispatch.
	rv := reflect.Value(target)
	//: cannot set a non-addressable target regardless of value shape.
	if !rv.CanSet() {
		//: surface as decode failure.
		return cannotSetError()
	}
	//: typed-nil source short-circuits — zero the destination.
	if value == nil {
		//: set to the zero value of the target's type.
		rv.Set(reflect.Zero(targetType))
		//: success.
		return nil
	}
	//: convert from decoded Go type to the target's reflect.Type.
	conv, cerr := convertValue(value, targetType)
	//: surface conversion failure.
	if cerr != nil {
		//: already wrapped.
		return cerr
	}
	//: publish through the pointer.
	rv.Set(conv)
	//: success.
	return nil
}

// convertValue narrows a decoded value into the target reflect.Type.
func convertValue(value any, target reflect.Type) (converted reflect.Value, err error) {
	//: any/interface{} target — keep the decoded type as-is.
	if target.Kind() == reflect.Interface && target.NumMethod() == 0 {
		//: nothing to convert.
		return reflect.ValueOf(value), nil
	}
	//: numeric narrowing path.
	if v, ok := narrowNumeric(value, target); ok {
		//: success.
		return v, nil
	}
	//: same-type assignability path.
	rv := reflect.ValueOf(value)
	//: direct match.
	if rv.Type().AssignableTo(target) {
		//: already aligned.
		return rv, nil
	}
	//: convertible types (e.g. named string).
	if rv.Type().ConvertibleTo(target) {
		//: explicit Convert.
		return rv.Convert(target), nil
	}
	//: composite projection — the decoder produces generic types
	//: (map[string]any, []any, map[any]any) for composite TLV
	//: records; project them into the caller's typed struct/slice/map.
	if conv, ok, perr := projectComposite(value, target); ok || perr != nil {
		//: applied path returns conv (possibly with err); non-applied
		//: falls through to the incompatible-type sentinel.
		return conv, perr
	}
	//: incompatible — surface as decode failure.
	return reflect.Value{}, incompatibleTypeError(rv.Type().String(), target.String())
}

// projectComposite handles composite-to-typed projections the decoder
// needs to make Marshal+Unmarshal a true roundtrip for typed
// struct/slice/map/pointer targets (the decoder builds map[string]any
// / []any / map[any]any from the wire; the helpers fit them to the
// caller's type). Returns (converted, true, nil) on a matched
// projection, (zero, false, nil) when no projection applies (caller
// falls through to the incompatible-type sentinel), or
// (zero, false, err) on a recursive projection failure.
func projectComposite(value any, target reflect.Type) (converted reflect.Value, applied bool, err error) {
	//: dispatch on the destination kind — that's what determines the
	//: shape we need to manufacture.
	switch target.Kind() {
	//: struct target ← map[string]any from decodeStruct.
	case reflect.Struct:
		//: small helper keeps cyclomatic + LOC under the linter caps.
		return projectCompositeStruct(value, target)
	//: slice target ← []any from decodeSlice.
	case reflect.Slice:
		//: small helper keeps cyclomatic + LOC under the linter caps.
		return projectCompositeSlice(value, target)
	//: map target ← map[any]any or map[string]any from decodeMap/decodeStruct.
	case reflect.Map:
		//: small helper keeps cyclomatic + LOC under the linter caps.
		return projectCompositeMap(value, target)
	//: pointer target — build the element, then box it.
	case reflect.Pointer:
		//: small helper keeps cyclomatic + LOC under the linter caps.
		return projectCompositePointer(value, target)
	//: every other kind — leave the fallback chain in place.
	default:
		//: no projection strategy for this kind.
		return reflect.Value{}, false, nil
	}
}

// projectCompositeStruct attempts the map[string]any → struct
// projection. Falls through (applied=false) when the source isn't the
// expected generic map shape.
func projectCompositeStruct(value any, target reflect.Type) (converted reflect.Value, applied bool, err error) {
	//: type-assert the source against the generic map decodeStruct
	//: produces; non-matching sources fall through to the caller's
	//: incompatible-type sentinel.
	m, ok := value.(map[string]any)
	//: source shape mismatch → no projection.
	if !ok {
		//: fall through; not applied.
		return reflect.Value{}, false, nil
	}
	//: per-field projection by exported sf.Name.
	conv, perr := projectMapToStruct(m, target)
	//: applied path — caller surfaces perr verbatim.
	return conv, true, perr
}

// projectCompositeSlice attempts the []any → typed slice projection.
func projectCompositeSlice(value any, target reflect.Type) (converted reflect.Value, applied bool, err error) {
	//: type-assert the source against the generic []any decodeSlice
	//: produces.
	s, ok := value.([]any)
	//: source shape mismatch → no projection.
	if !ok {
		//: fall through.
		return reflect.Value{}, false, nil
	}
	//: per-element projection.
	conv, perr := projectSliceToTyped(s, target)
	//: applied path.
	return conv, true, perr
}

// projectCompositeMap attempts the map[any]any / map[string]any →
// typed map projection. Reflect-based source walk handles both
// decoded map flavours uniformly.
func projectCompositeMap(value any, target reflect.Type) (converted reflect.Value, applied bool, err error) {
	//: defensive — guard against non-map sources so we never panic.
	if reflect.ValueOf(value).Kind() != reflect.Map {
		//: fall through.
		return reflect.Value{}, false, nil
	}
	//: per-entry key+value projection — projectMapToTyped accepts any
	//: so the linter doesn't pin the parameter to a concrete reflect
	//: type (KTN-API-MINIF).
	conv, perr := projectMapToTyped(value, target)
	//: applied path.
	return conv, true, perr
}

// projectCompositePointer recursively projects into target.Elem() and
// wraps the result in a fresh pointer.
func projectCompositePointer(value any, target reflect.Type) (converted reflect.Value, applied bool, err error) {
	//: project into the pointed-to type first.
	elem, ok, perr := projectComposite(value, target.Elem())
	//: bubble up a projection error verbatim.
	if perr != nil {
		//: not applied so caller can chain further fallbacks.
		return reflect.Value{}, false, perr
	}
	//: no projection matched at the element level either.
	if !ok {
		//: fall through.
		return reflect.Value{}, false, nil
	}
	//: allocate a new addressable pointee and wrap it in a pointer.
	ptr := reflect.New(target.Elem())
	//: publish the projected element through the pointer.
	ptr.Elem().Set(elem)
	//: applied path.
	return ptr, true, nil
}

// projectMapToStruct fills a struct of target type from the
// map[string]any produced by decodeStruct. Field matching is by
// exported field name — the inverse of collectStructFields which
// writes sf.Name. Uses cachedStructTypeInfo so the reflect.Type walk
// runs at most once per target type process-wide.
// Missing source fields leave the destination at its zero value;
// nil source values explicitly zero the destination.
func projectMapToStruct(src map[string]any, target reflect.Type) (converted reflect.Value, err error) {
	//: cached metadata — single reflect walk per type, then reused.
	ti := cachedStructTypeInfo(target)
	//: new addressable instance we can Set into.
	dst := reflect.New(target).Elem()
	//: walk the cached metadata in declaration order.
	for _, f := range ti.fields {
		//: look up the wire value by the cached field name.
		srcVal, found := src[f.name]
		//: silent absence — leave the destination field zero-valued.
		if !found {
			//: skip the field; reflect.New already zeroed it.
			continue
		}
		//: nil source explicitly zeroes the destination.
		if srcVal == nil {
			//: write the zero value of the field's cached type.
			dst.Field(f.index).Set(reflect.Zero(f.typ))
			//: next field.
			continue
		}
		//: recursive convertValue handles scalars + nested composites.
		conv, cerr := convertValue(srcVal, f.typ)
		//: bubble per-field projection failure verbatim.
		if cerr != nil {
			//: surface upward.
			return reflect.Value{}, cerr
		}
		//: publish the converted value into the struct field.
		dst.Field(f.index).Set(conv)
	}
	//: success.
	return dst, nil
}

// projectSliceToTyped fills a typed slice from the []any produced by
// decodeSlice. Element conversion goes through convertValue so nested
// composites (slice of struct, slice of map) recurse cleanly.
func projectSliceToTyped(src []any, target reflect.Type) (converted reflect.Value, err error) {
	//: snapshot the element type once.
	elemType := target.Elem()
	//: pre-allocate with the source length so reslicing is avoided.
	dst := reflect.MakeSlice(target, len(src), len(src))
	//: per-element conversion.
	for i, e := range src {
		//: nil element gets the zero value of the element type.
		if e == nil {
			//: write zero.
			dst.Index(i).Set(reflect.Zero(elemType))
			//: next element.
			continue
		}
		//: convertValue handles scalar narrowing + composite recursion.
		conv, cerr := convertValue(e, elemType)
		//: bubble per-element failure verbatim.
		if cerr != nil {
			//: surface upward.
			return reflect.Value{}, cerr
		}
		//: publish the element.
		dst.Index(i).Set(conv)
	}
	//: success.
	return dst, nil
}

// projectMapToTyped fills a typed map from any source value whose
// reflect.Kind is Map — covers both map[any]any (decodeMap output)
// and map[string]any (decodeStruct output). Reflect-based iteration
// handles both flavours without two branches. Accepting `any` keeps
// the parameter signature minimal (KTN-API-MINIF) — the caller's
// guard already confirmed src is map-shaped.
func projectMapToTyped(src any, target reflect.Type) (converted reflect.Value, err error) {
	//: reflect over the source once locally; caller already
	//: confirmed Kind() == Map so this is safe.
	rv := reflect.ValueOf(src)
	//: snapshot key + value types once.
	keyType := target.Key()
	//: value type is the codomain.
	valType := target.Elem()
	//: pre-allocate with source cardinality.
	dst := reflect.MakeMapWithSize(target, rv.Len())
	//: walk every entry.
	for _, k := range rv.MapKeys() {
		//: project the key into the target key type.
		kConv, kErr := convertValue(k.Interface(), keyType)
		//: bubble key conversion failure verbatim.
		if kErr != nil {
			//: surface upward.
			return reflect.Value{}, kErr
		}
		//: read the value through the reflect indirection.
		rawV := rv.MapIndex(k).Interface()
		//: nil value gets the zero value of the value type.
		if rawV == nil {
			//: write zero.
			dst.SetMapIndex(kConv, reflect.Zero(valType))
			//: next entry.
			continue
		}
		//: project the value into the target value type.
		vConv, vErr := convertValue(rawV, valType)
		//: bubble value conversion failure verbatim.
		if vErr != nil {
			//: surface upward.
			return reflect.Value{}, vErr
		}
		//: install the entry.
		dst.SetMapIndex(kConv, vConv)
	}
	//: success.
	return dst, nil
}

// narrowNumeric widens or narrows a decoded numeric value into target.
// Returns (reflect.Value{}, false) when the value is not numeric.
//
//nolint:gocyclo // numeric narrowing is intrinsically wide.
func narrowNumeric(value any, target reflect.Type) (converted reflect.Value, ok bool) {
	//: signed integer source.
	if n, isInt := value.(int64); isInt {
		//: route by target kind.
		return narrowFromInt(n, target)
	}
	//: unsigned integer source.
	if ui, isUint := value.(uint64); isUint {
		//: route by target kind.
		return narrowFromUint(ui, target)
	}
	//: float source.
	if f, isFloat := value.(float64); isFloat {
		//: route by target kind.
		return narrowFromFloat(f, target)
	}
	//: non-numeric.
	return reflect.Value{}, false
}

// narrowFromInt converts a decoded int64 into target's numeric Kind.
func narrowFromInt(n int64, target reflect.Type) (converted reflect.Value, ok bool) {
	//: numeric kinds (signed + unsigned) all go through Convert.
	switch target.Kind() {
	//: every integer Kind handled identically.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		//: stdlib Convert handles range narrowing / sign reinterpret.
		return reflect.ValueOf(n).Convert(target), true
	//: non-numeric target.
	default:
		//: caller falls back to AssignableTo / ConvertibleTo.
		return reflect.Value{}, false
	}
}

// narrowFromUint converts a decoded uint64 into target's numeric Kind.
func narrowFromUint(ui uint64, target reflect.Type) (converted reflect.Value, ok bool) {
	//: numeric kinds (signed + unsigned) all go through Convert.
	switch target.Kind() {
	//: every integer Kind handled identically.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		//: stdlib Convert handles range narrowing / sign reinterpret.
		return reflect.ValueOf(ui).Convert(target), true
	//: non-numeric target.
	default:
		//: caller falls back to AssignableTo / ConvertibleTo.
		return reflect.Value{}, false
	}
}

// narrowFromFloat converts a decoded float64 into target's float Kind.
func narrowFromFloat(f float64, target reflect.Type) (converted reflect.Value, ok bool) {
	//: route by target kind.
	switch target.Kind() {
	//: floats.
	case reflect.Float32, reflect.Float64:
		//: stdlib Convert handles the precision change.
		return reflect.ValueOf(f).Convert(target), true
	//: non-float target.
	default:
		//: caller falls back to AssignableTo / ConvertibleTo.
		return reflect.Value{}, false
	}
}

// truncatedError wraps a fresh TRUNCATED sentinel.
func truncatedError() error {
	//: no cause to wrap.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVTruncated,
		Reason:  "TRUNCATED",
		Public:  "TLV buffer truncated",
		Private: "service/codec/tlv: buffer ended before the declared record length was satisfied",
	})
}

// malformedLengthError wraps a fresh UNMARSHAL_FAILED sentinel that
// records the expected vs declared length.
func malformedLengthError(expected, got uint64) error {
	//: surface as decode failure with diagnostic fields.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv: declared length does not match the expected tag width",
	}, errs.String("expected", strconv.FormatUint(expected, decimalBase)), errs.String("got", strconv.FormatUint(got, decimalBase)))
}

// unknownTagError surfaces an unknown tag as a decode failure.
func unknownTagError(tag Tag) error {
	//: include the byte for diagnostics.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv: unknown tag byte",
	}, errs.Int("tag", int(tag)))
}

// sizeExceededError surfaces a length declaration above maxTLVBytes.
func sizeExceededError(length uint64) error {
	//: surface as SIZE_EXCEEDED.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVSizeExceeded,
		Reason:  "SIZE_EXCEEDED",
		Public:  "TLV input exceeds size limit",
		Private: "service/codec/tlv: declared length exceeds maxTLVBytes",
	}, errs.String("len", strconv.FormatUint(length, decimalBase)), errs.Int("cap", maxTLVBytes))
}

// cannotSetError surfaces an unsettable target.
func cannotSetError() error {
	//: surface as decode failure.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV codec requires a settable pointer target",
		Private: "service/codec/tlv.assignToPointer: target is not settable",
	})
}

// unhashableKeyError surfaces a non-comparable map key.
func unhashableKeyError() error {
	//: surface as decode failure.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv: decoded map key is not comparable",
	})
}

// nonStringFieldNameError surfaces a struct field name that did not
// decode to a Go string.
func nonStringFieldNameError() error {
	//: surface as decode failure.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv.decodeStruct: field name is not a string",
	})
}

// fieldNameTooLongDecodeError surfaces a wire-declared field-name length
// above maxFieldNameBytes. Mirrors the encoder-side fieldNameTooLong
// guard so the decoder rejects crafted payloads before allocating the
// name buffer (CWE-400 defence).
func fieldNameTooLongDecodeError(length uint64) error {
	//: surface as decode failure with diagnostic fields.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv.decodeFieldName: declared field name length exceeds maxFieldNameBytes",
	}, errs.String("len", strconv.FormatUint(length, decimalBase)), errs.Int("cap", maxFieldNameBytes))
}

// nonPointerTargetError surfaces a Unmarshal call with a non-pointer or
// nil-pointer target.
func nonPointerTargetError() error {
	//: surface as decode failure.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV codec requires a non-nil pointer target",
		Private: "service/codec/tlv.Unmarshal: target is not a non-nil pointer",
	})
}

// trailingBytesError surfaces an Unmarshal with bytes left after the
// first record.
func trailingBytesError(trailing int) error {
	//: surface as decode failure for callers that match by reason.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv.Unmarshal: trailing bytes after the first record",
	}, errs.Int("trailing", trailing))
}

// varintOverflowError surfaces a malformed LEB128 length.
func varintOverflowError() error {
	//: surface as decode failure.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv: varint length overflow",
	})
}

// incompatibleTypeError surfaces a value that cannot be assigned/converted
// into the target reflect.Type.
func incompatibleTypeError(from, to string) error {
	//: include both types for diagnostics.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TLV decoding failed",
		Private: "service/codec/tlv.convertValue: decoded type not assignable to target",
	}, errs.String("from", from), errs.String("to", to))
}
