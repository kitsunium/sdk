// Package tlv — reflection-driven encoder shared by Marshal, Append, and
// the streaming Encoder. Every helper returns the (possibly re-allocated)
// destination slice plus a wrapped *errs.Error on failure.
package tlv

import (
	"encoding/binary"
	"io"
	"math"
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tlvEncoder adapts an io.Writer to codec.Encoder. Each Encode call emits
// exactly one independent TLV record.
type tlvEncoder struct {
	w io.Writer
}

// Encode serialises v as a single TLV record and writes it to the wrapped
// writer.
func (e *tlvEncoder) Encode(v any) error {
	//: encode into a scratch buffer first so a partial write never leaves
	//: a torn record on the wire.
	buf, eerr := encodeValue(nil, v, 0)
	//: surface any encode failure verbatim.
	if eerr != nil {
		//: nothing to flush to the writer.
		return eerr
	}
	//: hand the whole record to the writer in one shot.
	n, werr := e.w.Write(buf)
	//: detect partial writes — Go's io.Writer contract permits n < len(buf)
	//: with a nil error, but a torn TLV record on the wire is unrecoverable.
	//: Surface as io.ErrShortWrite so callers can errors.Is() against it.
	if werr == nil && n != len(buf) {
		werr = io.ErrShortWrite
	}
	//: success fast-path.
	if werr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the writer error as a marshal failure.
	return errs.Wrap(werr, errs.WrapParams{
		Code:    CodeTLVMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "TLV encoding failed",
		Private: "service/codec/tlv.Encoder.Encode: writer error or short write",
	})
}

// Close is a no-op — the encoder does not own the writer.
func (*tlvEncoder) Close() error {
	//: nothing to flush.
	return nil
}

// appendTagLen writes the 1-byte tag followed by the LEB128 length.
func appendTagLen(dst []byte, tag Tag, length uint64) []byte {
	//: emit the tag byte first.
	dst = append(dst, byte(tag))
	//: then the LEB128 length.
	return binary.AppendUvarint(dst, length)
}

// encodeValue is the main reflection dispatch. depth grows on every
// recursive descent into a composite (slice/map/struct).
func encodeValue(dst []byte, v any, depth int) (encoded []byte, err error) {
	//: nesting guard runs before any reflection work.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return dst, depthExceededError(depth)
	}
	//: untyped nil short-circuits before reflect would crash.
	if v == nil {
		//: typed-nil sentinel record.
		return appendTagLen(dst, tagNil, 0), nil
	}
	//: drill through any number of pointer / interface indirections.
	rv := reflect.ValueOf(v)
	//: dereference pointers/interfaces; nil indirections become tagNil.
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		//: nil intermediate counts as a typed nil.
		if rv.IsNil() {
			//: emit the nil record and stop.
			return appendTagLen(dst, tagNil, 0), nil
		}
		//: peel one level of indirection.
		rv = rv.Elem()
	}
	//: route to the family-specific helper by Kind.
	return encodeByKind(dst, reflectView(rv), depth)
}

// encodeByKind picks the right family encoder for the value's reflect.Kind.
// Splits into scalar / composite sub-dispatch so each function stays
// under the linter's cyclomatic-complexity budget. A scalar Kind returns
// (out, true, nil); a composite Kind returns (out, false, nil) and is
// retried by tryEncodeComposite; an unsupported Kind surfaces the error.
func encodeByKind(dst []byte, view reflectView, depth int) (encoded []byte, err error) {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: scalars first.
	if scalarOut, handled := tryEncodeScalar(dst, view); handled {
		//: dispatched as scalar — scalars never fail.
		return scalarOut, nil
	}
	//: composites next.
	if compositeOut, handled, compositeErr := tryEncodeComposite(dst, view, depth); handled {
		//: dispatched as composite.
		return compositeOut, compositeErr
	}
	//: rejected kinds: Chan, Func, Complex64/128, UnsafePointer, Invalid.
	return dst, unsupportedKind(rv.Kind().String())
}

// tryEncodeScalar dispatches every fixed-shape primitive Kind (bool,
// integers, floats, string). Returns handled=false on composite Kinds.
//
//nolint:gocyclo // primitive dispatch table.
func tryEncodeScalar(dst []byte, view reflectView) (encoded []byte, handled bool) {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: dispatch on the resolved Kind.
	switch rv.Kind() {
	//: boolean.
	case reflect.Bool:
		//: zero-payload record.
		return encodeBool(dst, rv.Bool()), true
	//: signed integers fall through to a single helper.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: width selection happens inside.
		return encodeInt(dst, rv.Int()), true
	//: unsigned integers fall through to a single helper.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: width selection happens inside.
		return encodeUint(dst, rv.Uint()), true
	//: IEEE-754 floats — width follows the source Kind.
	case reflect.Float32:
		//: 32-bit float-bits payload.
		return encodeFloat32(dst, float32(rv.Float())), true
	//: 64-bit float.
	case reflect.Float64:
		//: 64-bit float-bits payload.
		return encodeFloat64(dst, rv.Float()), true
	//: UTF-8 string.
	case reflect.String:
		//: length prefix is the byte count.
		return encodeString(dst, rv.String()), true
	//: composite or rejected.
	default:
		//: caller fans out further.
		return dst, false
	}
}

// tryEncodeComposite dispatches every composite Kind (slice, array, map,
// struct). Returns handled=false on scalar or rejected Kinds. err is
// the last named return so the lint rule does not flag it.
func tryEncodeComposite(dst []byte, view reflectView, depth int) (encoded []byte, handled bool, err error) {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: dispatch on the resolved Kind.
	switch rv.Kind() {
	//: []byte takes the tagBytes fast path; everything else goes to slice/array.
	case reflect.Slice, reflect.Array:
		//: route to the slice/array helper.
		out, eerr := encodeCompositeSliceLike(dst, view, depth)
		//: composite handled.
		return out, true, eerr
	//: map of any TLV-encodable key type.
	case reflect.Map:
		//: collect pairs as []any (alternating key, value) and recurse.
		out, eerr := encodeMapPairs(dst, collectMapPairs(view), depth)
		//: composite handled.
		return out, true, eerr
	//: struct uses exported fields only.
	case reflect.Struct:
		//: collect named exported fields and recurse.
		out, eerr := encodeStructEntries(dst, collectStructFields(view), depth)
		//: composite handled.
		return out, true, eerr
	//: scalar or rejected.
	default:
		//: caller surfaces the rejection.
		return dst, false, nil
	}
}

// encodeCompositeSliceLike handles the slice/array branch of the
// composite dispatch table. Splits the []byte fast path from the generic
// element-by-element path.
func encodeCompositeSliceLike(dst []byte, view reflectView, depth int) (encoded []byte, err error) {
	//: convert once for type inspection.
	rv := reflect.Value(view)
	//: byte slice/array fast path.
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		//: reuse the bytes payload helper.
		return encodeBytes(dst, bytesFromReflectValue(view)), nil
	}
	//: generic slice — collect elements as []any and recurse.
	return encodeSliceElements(dst, collectSliceElements(view), depth)
}

// encodeBool emits a 1-byte tag-only record for the boolean value.
func encodeBool(dst []byte, b bool) []byte {
	//: tagBoolTrue vs tagBoolFalse — length is always zero.
	if b {
		//: true branch.
		return appendTagLen(dst, tagBoolTrue, 0)
	}
	//: false branch.
	return appendTagLen(dst, tagBoolFalse, 0)
}

// encodeInt writes the narrowest signed-integer record fitting n.
func encodeInt(dst []byte, n int64) []byte {
	//: int8 range.
	if n >= math.MinInt8 && n <= math.MaxInt8 {
		//: 1-byte payload.
		dst = appendTagLen(dst, tagInt8, uint64(width8))
		return append(dst, byte(int8(n))) //nolint:gosec // range-checked above
	}
	//: int16 range.
	if n >= math.MinInt16 && n <= math.MaxInt16 {
		//: 2-byte big-endian payload.
		dst = appendTagLen(dst, tagInt16, uint64(width16))
		return binary.BigEndian.AppendUint16(dst, uint16(int16(n))) //nolint:gosec // range-checked above
	}
	//: int32 range.
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		//: 4-byte big-endian payload.
		dst = appendTagLen(dst, tagInt32, uint64(width32))
		return binary.BigEndian.AppendUint32(dst, uint32(int32(n))) //nolint:gosec // range-checked above
	}
	//: full int64 fallback.
	dst = appendTagLen(dst, tagInt64, uint64(width64))
	return binary.BigEndian.AppendUint64(dst, uint64(n)) //nolint:gosec // wire-format reinterpret
}

// encodeUint writes the narrowest unsigned-integer record fitting ui.
func encodeUint(dst []byte, ui uint64) []byte {
	//: uint8 range.
	if ui <= math.MaxUint8 {
		//: emit the 1-byte payload header.
		dst = appendTagLen(dst, tagUint8, uint64(width8))
		//: append the single payload byte.
		return append(dst, byte(ui))
	}
	//: uint16 range.
	if ui <= math.MaxUint16 {
		//: emit the 2-byte payload header.
		dst = appendTagLen(dst, tagUint16, uint64(width16))
		//: append the big-endian uint16 payload.
		return binary.BigEndian.AppendUint16(dst, uint16(ui))
	}
	//: uint32 range.
	if ui <= math.MaxUint32 {
		//: emit the 4-byte payload header.
		dst = appendTagLen(dst, tagUint32, uint64(width32))
		//: append the big-endian uint32 payload.
		return binary.BigEndian.AppendUint32(dst, uint32(ui))
	}
	//: emit the 8-byte payload header.
	dst = appendTagLen(dst, tagUint64, uint64(width64))
	//: append the big-endian uint64 payload.
	return binary.BigEndian.AppendUint64(dst, ui)
}

// encodeFloat32 emits a 4-byte IEEE-754 single record.
func encodeFloat32(dst []byte, f float32) []byte {
	//: emit the 4-byte payload header.
	dst = appendTagLen(dst, tagFloat32, uint64(width32))
	//: append the big-endian Float32bits payload.
	return binary.BigEndian.AppendUint32(dst, math.Float32bits(f))
}

// encodeFloat64 emits an 8-byte IEEE-754 double record.
func encodeFloat64(dst []byte, f float64) []byte {
	//: emit the 8-byte payload header.
	dst = appendTagLen(dst, tagFloat64, uint64(width64))
	//: append the big-endian Float64bits payload.
	return binary.BigEndian.AppendUint64(dst, math.Float64bits(f))
}

// encodeString emits a UTF-8 string record. length = byte count.
func encodeString(dst []byte, s string) []byte {
	//: length prefix is the byte count, not the rune count.
	dst = appendTagLen(dst, tagString, uint64(len(s)))
	//: payload is the raw bytes.
	return append(dst, s...)
}

// encodeBytes emits an opaque byte sequence record.
func encodeBytes(dst, payload []byte) []byte {
	//: length prefix is the byte count.
	dst = appendTagLen(dst, tagBytes, uint64(len(payload)))
	//: payload is the raw bytes.
	return append(dst, payload...)
}

// collectSliceElements materialises a generic slice/array as []any so
// downstream encoders never need to handle reflect.Value directly.
func collectSliceElements(view reflectView) []any {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: pre-allocate the exact capacity so append never grows the slice.
	collected := make([]any, 0, rv.Len())
	//: copy each element through Interface().
	for i := range rv.Len() {
		//: append the boxed Go value.
		collected = append(collected, rv.Index(i).Interface())
	}
	//: caller iterates in order.
	return collected
}

// collectMapPairs materialises a map as a flat []any of alternating
// (key, value) entries. The order matches reflect.MapRange's emission.
func collectMapPairs(view reflectView) []any {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: pre-allocate to (mapPairStride * Len) so the slice never grows.
	out := make([]any, 0, mapPairStride*rv.Len())
	//: iterate via MapRange to honour the runtime's unordered guarantee.
	iter := rv.MapRange()
	//: each entry contributes key and value.
	for iter.Next() {
		//: append the key.
		out = append(out, iter.Key().Interface())
		//: append the value.
		out = append(out, iter.Value().Interface())
	}
	//: caller iterates pair-by-pair.
	return out
}

// collectStructFields extracts every exported field as a (name, value)
// pair flattened into []any. Unexported fields are silently dropped.
func collectStructFields(view reflectView) []any {
	//: convert once for method dispatch.
	rv := reflect.Value(view)
	//: pre-allocate the worst-case length (2 entries per field).
	t := rv.Type()
	out := make([]any, 0, fieldEntryStride*t.NumField())
	//: walk each field.
	for i := range t.NumField() {
		//: skip unexported names.
		sf := t.Field(i)
		//: PkgPath is empty for exported names.
		if sf.PkgPath != "" {
			//: not exported.
			continue
		}
		//: append the (name, value) entries.
		out = append(out, sf.Name)
		//: value follows the name.
		out = append(out, rv.Field(i).Interface())
	}
	//: caller iterates in declaration order, stepping by fieldEntryStride.
	return out
}

// encodeSliceElements emits a tagSlice record from the pre-collected
// element list.
func encodeSliceElements(dst []byte, elements []any, depth int) (encoded []byte, err error) {
	//: emit the slice header.
	dst = appendTagLen(dst, tagSlice, uint64(len(elements)))
	//: encode each element in order; failure aborts the whole record.
	for _, el := range elements {
		//: encode the element with depth+1.
		next, eerr := encodeValue(dst, el, depth+1)
		//: surface any per-element failure.
		if eerr != nil {
			//: caller will roll dst back to its prior length.
			return dst, eerr
		}
		//: advance the buffer pointer.
		dst = next
	}
	//: success path.
	return dst, nil
}

// encodeMapPairs emits a tagMap record from the pre-collected flat
// (key, value, key, value, …) slice.
func encodeMapPairs(dst []byte, pairs []any, depth int) (encoded []byte, err error) {
	//: pair count is half the flat length.
	dst = appendTagLen(dst, tagMap, uint64(len(pairs)/mapPairStride))
	//: walk pairs of two.
	for i := 0; i < len(pairs); i += mapPairStride {
		//: encode the key first.
		next, kerr := encodeValue(dst, pairs[i], depth+1)
		//: surface key failure.
		if kerr != nil {
			//: caller will roll dst back to its prior length.
			return dst, kerr
		}
		//: encode the value second.
		next2, verr := encodeValue(next, pairs[i+1], depth+1)
		//: surface value failure.
		if verr != nil {
			//: caller will roll dst back to its prior length.
			return dst, verr
		}
		//: advance the buffer pointer.
		dst = next2
	}
	//: success path.
	return dst, nil
}

// encodeStructEntries emits a tagStruct record from the pre-collected
// flat (name, value, name, value, …) entry slice.
func encodeStructEntries(dst []byte, entries []any, depth int) (encoded []byte, err error) {
	//: field count is half the flat length.
	dst = appendTagLen(dst, tagStruct, uint64(len(entries)/fieldEntryStride))
	//: emit each (name-TLV, value-TLV) pair.
	for i := 0; i < len(entries); i += fieldEntryStride {
		//: extract the (typed) field name.
		name, _ := entries[i].(string)
		//: enforce the field-name byte cap.
		if len(name) > maxFieldNameBytes {
			//: surface a marshal failure rather than silently truncating.
			return dst, fieldNameTooLong(len(name))
		}
		//: the name is always a tagString TLV.
		dst = encodeString(dst, name)
		//: the value descends one level deeper.
		next, verr := encodeValue(dst, entries[i+1], depth+1)
		//: surface any value-encode failure.
		if verr != nil {
			//: caller will roll dst back to its prior length.
			return dst, verr
		}
		//: advance the buffer pointer.
		dst = next
	}
	//: success path.
	return dst, nil
}

// unsupportedKind wraps an UNSUPPORTED_TYPE sentinel with the offending kind.
func unsupportedKind(kind string) error {
	//: kind names are short and stable across Go versions.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVUnsupportedType,
		Reason:  "UNSUPPORTED_TYPE",
		Public:  "TLV codec cannot encode this type",
		Private: "service/codec/tlv: reflect.Kind not supported on the wire",
	}, errs.String("kind", kind))
}

// depthExceededError wraps a fresh DEPTH_EXCEEDED sentinel.
func depthExceededError(depth int) error {
	//: surface the documented depth sentinel.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVDepthExceeded,
		Reason:  "DEPTH_EXCEEDED",
		Public:  "TLV nesting depth exceeds limit",
		Private: "service/codec/tlv: depth exceeded maxTLVDepth",
	}, errs.Int("depth", depth), errs.Int("cap", maxTLVDepth))
}

// fieldNameTooLong wraps a MARSHAL_FAILED for an over-long field name.
func fieldNameTooLong(length int) error {
	//: include the offending length in Fields for diagnostics.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeTLVMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "TLV struct field name too long",
		Private: "service/codec/tlv.encodeStructField: name exceeds maxFieldNameBytes",
	}, errs.Int("len", length), errs.Int("cap", maxFieldNameBytes))
}
