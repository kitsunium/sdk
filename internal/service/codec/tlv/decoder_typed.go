// Package tlv — target-aware decode fast path.
//
// The default decoder (decoder.go) produces an untyped value tree
// (map[string]any for structs, []any for slices, map[any]any for maps)
// then projects it onto the caller's typed target. For typed-struct
// targets this pays two costs: the outer map[string]any allocation +
// per-field string keys, AND a second walk through projectMapToStruct.
//
// decodeStructInto walks the wire ONCE, locating each field by name
// in the cached structTypeInfo and assigning into target.Field(i)
// directly. The result is byte-identical to the untyped path on
// roundtrip; only the in-flight allocation profile changes.
//
// Scope: top-level *struct targets only. Nested struct fields still
// fall through the untyped decodeValue + convertValue helpers so
// cross-shape narrowings (e.g. map[string]any field on a typed
// struct) remain compatible. Extending the fast path to nested
// structs is a follow-up that reuses the same primitives.
package tlv

import (
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// linearScanFieldLimit is the field-count threshold above which a
// name→index map beats a linear scan. Determined empirically: at
// 16 fields the map's overhead amortises across enough lookups; below
// that, the linear scan's branch predictor wins.
const linearScanFieldLimit int = 16

// notFoundFieldIdx is the sentinel resolveFieldIndex returns when a
// wire field name does not match any struct field. Decoders treat this
// as "skip the value" rather than an error (mirrors json's lenient
// unknown-field handling). Keeping it as a named const keeps the
// sentinel intent visible at every call site instead of relying on the
// bare -1 literal.
const notFoundFieldIdx int = -1

// tryDecodeRootInto attempts the target-aware fast path. Returns
// handled=true when the typed path applied (success or error);
// handled=false signals "this target shape doesn't benefit from the
// fast path — caller falls through to decodeRoot's untyped walk".
//
// The Phase-1 scope is top-level *struct targets: that's where the
// untyped path's map[string]any + projectMapToStruct double-walk
// costs the most.
func tryDecodeRootInto(data []byte, target reflect.Value) (handled bool, err error) {
	//: combine the target-shape gates so the linter's MERGEGUARD
	//: rule is satisfied (both branches return the same sentinel).
	if target.Kind() != reflect.Struct || !target.CanSet() {
		//: signal caller to fall back to the untyped decodeRoot path.
		return false, nil
	}
	//: peek the tag — if the wire isn't a struct record, fall back.
	//: This keeps us compatible with payloads that encode a struct
	//: target from a non-struct source (e.g. map[string]any wire).
	if len(data) < minRecordBytes {
		//: surface the truncated sentinel via the typed path so the
		//: caller's untyped retry doesn't double-report.
		return true, truncatedError()
	}
	//: tagStruct on wire AND struct on target → fast path engages.
	if Tag(data[0]) != tagStruct {
		//: shape mismatch; let the untyped path project from any.
		return false, nil
	}
	//: parse the struct header (field count + cap-discard) and
	//: dispatch into decodeStructInto.
	residual, derr := decodeStructFromHeader(data[1:], target)
	//: typed-path always reports handled=true once the wire is a
	//: tagStruct (caller doesn't need to retry the untyped path).
	if derr != nil {
		//: already wrapped.
		return true, derr
	}
	//: typed path expects an exact buffer (Unmarshal semantics).
	if len(residual) != 0 {
		//: trailing bytes sentinel matches the untyped path's behaviour.
		return true, trailingBytesError(len(residual))
	}
	//: typed-path success.
	return true, nil
}

// decodeStructFromHeader reads the LEB128 field-count header at rest
// and dispatches into decodeStructInto. Hoisted out of
// tryDecodeRootInto so that function stays under the cyclomatic
// budget.
func decodeStructFromHeader(rest []byte, target reflect.Value) (residual []byte, err error) {
	//: read the field count.
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure verbatim.
	if lerr != nil {
		//: already wrapped.
		return rest, lerr
	}
	//: cap-discard the structural length before walking the fields.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return rest, sizeExceededError(length)
	}
	//: walk the wire and assign each field into target in place.
	return decodeStructInto(length, rest, target, 0)
}

// decodeStructInto walks `length` field records starting at rest and
// assigns each by name into target. Unknown fields are silently
// skipped (their value bytes are still consumed via decodeValue so the
// cursor advances). Per-field assignment goes through convertValue so
// cross-shape conversions (e.g. int8 wire → int field) keep working
// without duplicating the narrowing logic.
func decodeStructInto(length uint64, rest []byte, target reflect.Value, depth int) (residual []byte, err error) {
	//: cached metadata — single reflect walk per type, then reused.
	info := cachedStructTypeInfo(target.Type())
	//: build a local name→fields-index map only when the field count
	//: justifies the cost (linear scan is faster for ≤16 fields).
	nameIndex := buildNameIndex(info)
	//: walk every field record advertised on the wire.
	for range length {
		//: read the field name.
		name, next, nerr := decodeFieldName(rest, depth+1)
		//: surface name failure verbatim.
		if nerr != nil {
			//: already wrapped.
			return rest, nerr
		}
		//: advance the cursor past the name.
		rest = next
		//: resolve the wire name to a struct field index (notFoundFieldIdx
		//: when the wire field has no struct counterpart).
		fieldIdx, _ := resolveFieldIndex(info, nameIndex, name)
		//: decode the value either into the typed field or untyped.
		next2, ferr := decodeFieldValue(rest, target, info, fieldIdx, depth+1)
		//: surface per-field failure verbatim.
		if ferr != nil {
			//: already wrapped.
			return rest, ferr
		}
		//: advance past the field's value record.
		rest = next2
	}
	//: walked every field; hand the residual back to the caller.
	return rest, nil
}

// buildNameIndex returns a name→fields-index map when the struct has
// enough fields to justify the map's allocation, otherwise nil so
// callers fall back to a linear scan (faster for small structs).
func buildNameIndex(info *structTypeInfo) map[string]int {
	//: linear scan is faster than a map lookup at small fanout.
	if len(info.fields) <= linearScanFieldLimit {
		//: nil tells resolveFieldIndex to use linear scan.
		return nil
	}
	//: pre-size the map exactly to avoid grow during construction.
	out := make(map[string]int, len(info.fields))
	//: populate name→index for O(1) field lookup.
	for i, f := range info.fields {
		//: map by the cached exported name.
		out[f.name] = i
	}
	//: caller uses this for resolveFieldIndex.
	return out
}

// resolveFieldIndex returns the struct-field index for the wire name.
// Uses the optional nameIndex map for large structs, otherwise scans
// the cached field list linearly.
func resolveFieldIndex(info *structTypeInfo, nameIndex map[string]int, name string) (idx int, found bool) {
	//: large-struct path — single map lookup.
	if nameIndex != nil {
		//: comma-ok mirrors the linear scan's return.
		i, ok := nameIndex[name]
		//: miss returns -1 to match the linear-scan path bit-for-bit
		//: (Go's zero-value for int would otherwise be 0, which is a
		//: valid field index — confusing for callers and tests).
		if !ok {
			//: not found in the cache.
			return -1, false
		}
		//: success when the name is in the cache.
		return i, true
	}
	//: small-struct linear scan — cache hit on the common case.
	for i := range info.fields {
		//: exported field names are unique per struct (Go spec).
		if info.fields[i].name == name {
			//: name matched — caller assigns into target.Field(i).
			return i, true
		}
	}
	//: unknown field; caller skips the value via decodeValue.
	return -1, false
}

// decodeFieldValue routes a single field's value record either into
// the typed struct field (when fieldIdx != notFoundFieldIdx) or
// through the untyped decode (when the wire field has no matching
// struct field). The sentinel-based "found" signal keeps the
// signature flat and under the linter's MAXPARAM budget.
func decodeFieldValue(rest []byte, target reflect.Value, info *structTypeInfo, fieldIdx, depth int) (next []byte, err error) {
	//: unknown field on the wire — skip its value to keep the cursor
	//: aligned with the next field record. Mirrors encoding/json's
	//: lenient "unknown field = discard" semantics.
	if fieldIdx == notFoundFieldIdx {
		//: decodeValue returns the parsed any + residual; we discard.
		_, next2, verr := decodeValue(rest, depth)
		//: surface decode failure verbatim.
		if verr != nil {
			//: already wrapped.
			return rest, verr
		}
		//: cursor advances past the unknown field's value.
		return next2, nil
	}
	//: decode the field's value into a temporary `any` then convert
	//: into the typed struct field via the existing narrowing path.
	val, next2, verr := decodeValue(rest, depth)
	//: surface decode failure verbatim.
	if verr != nil {
		//: already wrapped.
		return rest, verr
	}
	//: assign into target.Field(i) using the cached field metadata.
	if aerr := assignFieldValue(reflectView(target), info, fieldIdx, val); aerr != nil {
		//: already wrapped.
		return rest, aerr
	}
	//: cursor advances past the consumed value record.
	return next2, nil
}

// assignFieldValue writes a decoded value into target.Field(i) using
// the cached field metadata so the helper doesn't pay reflect.Field
// + reflect.StructField lookup on every call. target is wrapped in
// reflectView to keep the public signature free of an externally-
// named concrete type (KTN-API-MINIF).
func assignFieldValue(target reflectView, info *structTypeInfo, fieldIdx int, val any) error {
	//: snapshot the field metadata once.
	field := info.fields[fieldIdx]
	//: addressable settable field for direct writes.
	dst := reflect.Value(target).Field(field.index)
	//: nil source → zero out the typed destination.
	if val == nil {
		//: write the field's zero value.
		dst.Set(reflect.Zero(field.typ))
		//: success.
		return nil
	}
	//: convert through the shared narrowing helper.
	conv, cerr := convertValue(val, field.typ)
	//: surface per-field conversion failure verbatim.
	if cerr != nil {
		//: wrap once more so the caller sees a typed unmarshal error.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeTLVUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "TLV decoding failed",
			Private: "service/codec/tlv.decodeStructInto: field conversion failed",
		}, errs.Int("field-index", field.index))
	}
	//: publish the converted value into the struct field.
	dst.Set(conv)
	//: success.
	return nil
}
