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
// Phase 1 — top-level *struct targets: that's where the untyped
// path's map[string]any + projectMapToStruct double-walk costs the
// most.
//
// Phase 2 — top-level *[]struct targets: each element re-enters the
// struct fast path so an N-element slice of structs avoids N map
// allocations + N projections, just like the single-struct case but
// linearly scaled.
func tryDecodeRootInto(data []byte, target reflect.Value) (handled bool, err error) {
	//: target must be settable; the untyped path's diagnostic is clearer.
	if !target.CanSet() {
		//: signal caller to fall back.
		return false, nil
	}
	//: dispatch on target.Kind() — struct, slice-of-struct, and map each
	//: get their own typed entry point; anything else falls through to
	//: the untyped projector.
	switch target.Kind() {
	//: top-level struct — Phase 1 path.
	case reflect.Struct:
		//: hand off to the struct dispatcher.
		return tryDecodeRootIntoStruct(data, target)
	//: top-level []Struct — Phase 2 path (slice of structs only).
	case reflect.Slice:
		//: handle only slice-of-struct here; other element kinds keep
		//: falling through to the untyped projector.
		if target.Type().Elem().Kind() != reflect.Struct {
			//: slice-of-non-struct uses the untyped path.
			return false, nil
		}
		//: typed slice-of-struct dispatcher.
		return tryDecodeRootIntoSliceOfStruct(data, target)
	//: top-level map[K]V — Phase 4 path. Skips the map[any]any
	//: intermediate the untyped projector builds.
	case reflect.Map:
		//: typed map dispatcher; falls back when wire shape disagrees.
		return tryDecodeRootIntoMap(data, target)
	//: every other root target uses the untyped path.
	default:
		//: signal caller to fall back.
		return false, nil
	}
}

// tryDecodeRootIntoMap handles the Phase-4 *map[K]V target path.
// Each (key, value) pair is decoded via the untyped walker and
// narrowed into K and V respectively; the map[any]any intermediate
// the legacy projector builds is skipped entirely.
func tryDecodeRootIntoMap(data []byte, target reflect.Value) (handled bool, err error) {
	//: peek the tag — only tagMap triggers the typed map path.
	if len(data) < minRecordBytes {
		//: surface the truncated sentinel via the typed path.
		return true, truncatedError()
	}
	//: shape mismatch — let the untyped path project from map[any]any.
	if Tag(data[0]) != tagMap {
		//: signal fall-back.
		return false, nil
	}
	//: read the pair count.
	rest := data[1:]
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure verbatim.
	if lerr != nil {
		//: already wrapped.
		return true, lerr
	}
	//: cap-discard structural length before walking pairs.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return true, sizeExceededError(length)
	}
	//: walk every pair directly into the typed map.
	residual, derr := decodeMapInto(length, rest, reflectView(target), 0)
	//: surface pair-walk failure verbatim.
	if derr != nil {
		//: already wrapped.
		return true, derr
	}
	//: typed path expects an exact buffer.
	if len(residual) != 0 {
		//: trailing bytes sentinel.
		return true, trailingBytesError(len(residual))
	}
	//: typed-path success.
	return true, nil
}

// decodeMapInto consumes `length` (key, value) records from rest and
// builds the typed map in place. Each key + value pair goes through
// the untyped walker then convertValue so cross-shape narrowing
// (e.g. tagInt8 wire → int field) keeps working without code dup.
func decodeMapInto(length uint64, rest []byte, targetView reflectView, depth int) (residual []byte, err error) {
	//: unwrap once for method dispatch (Type/Key/Elem/Set).
	target := reflect.Value(targetView)
	//: depth guard fires before any allocation.
	if depth+1 > maxTLVDepth {
		//: surface the documented depth sentinel.
		return rest, depthExceededError(depth + 1)
	}
	//: snapshot key + value types once.
	keyType := target.Type().Key()
	valType := target.Type().Elem()
	//: pre-allocate via the clamped hint to defuse declared-length DoS.
	out := reflect.MakeMapWithSize(target.Type(), sliceHint(length))
	//: walk every (key, value) pair on the wire.
	for range length {
		//: decode the key via the untyped walker.
		keyVal, next, kerr := decodeValue(rest, depth+1)
		//: surface key failure verbatim.
		if kerr != nil {
			//: already wrapped.
			return rest, kerr
		}
		//: decode the value next.
		valVal, next2, verr := decodeValue(next, depth+1)
		//: surface value failure verbatim.
		if verr != nil {
			//: already wrapped.
			return rest, verr
		}
		//: narrow + publish the pair.
		if perr := assignMapPair(reflectView(out), keyType, valType, keyVal, valVal); perr != nil {
			//: already wrapped.
			return rest, perr
		}
		//: advance past the consumed pair.
		rest = next2
	}
	//: publish through the caller's map pointer.
	target.Set(out)
	//: walked every pair; hand back the residual.
	return rest, nil
}

// assignMapPair narrows (key, value) into (keyType, valType) and
// publishes into out via SetMapIndex. Hoisted from decodeMapInto so
// that helper stays under the linter's MAXLOC + CYCLO budgets.
func assignMapPair(outView reflectView, keyType, valType reflect.Type, keyVal, valVal any) error {
	//: unwrap once for SetMapIndex dispatch.
	out := reflect.Value(outView)
	//: narrow the key.
	kConv, kerr := convertValue(keyVal, keyType)
	//: surface key conversion failure verbatim.
	if kerr != nil {
		//: already wrapped by convertValue.
		return kerr
	}
	//: narrow the value.
	vConv, verr := convertValue(valVal, valType)
	//: surface value conversion failure verbatim.
	if verr != nil {
		//: already wrapped by convertValue.
		return verr
	}
	//: publish the pair into the destination map.
	out.SetMapIndex(kConv, vConv)
	//: success.
	return nil
}

// tryDecodeRootIntoStruct handles the Phase-1 *struct target path.
// Verifies the wire carries a tagStruct record then dispatches into
// decodeStructFromHeader; falls back (handled=false) on any shape
// mismatch so the untyped projector retains its current behaviour.
func tryDecodeRootIntoStruct(data []byte, target reflect.Value) (handled bool, err error) {
	//: peek the tag — if the wire isn't a struct record, fall back.
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
	residual, derr := decodeStructFromHeader(data[1:], target, 0)
	//: surface decode failure verbatim.
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

// tryDecodeRootIntoSliceOfStruct handles the Phase-2 *[]Struct
// target path. Each element re-enters decodeStructInto so the
// per-element map[string]any allocations are avoided across the
// whole slice.
func tryDecodeRootIntoSliceOfStruct(data []byte, target reflect.Value) (handled bool, err error) {
	//: peek the tag — only tagSlice triggers the typed slice path.
	if len(data) < minRecordBytes {
		//: surface the truncated sentinel via the typed path.
		return true, truncatedError()
	}
	//: shape mismatch on the wire side; let the untyped path project.
	if Tag(data[0]) != tagSlice {
		//: shape mismatch; let the untyped path project from []any.
		return false, nil
	}
	//: read the element count.
	rest := data[1:]
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure verbatim.
	if lerr != nil {
		//: already wrapped.
		return true, lerr
	}
	//: cap-discard the structural length before walking elements.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return true, sizeExceededError(length)
	}
	//: walk each element directly into the slice.
	residual, derr := decodeSliceOfStructInto(length, rest, reflectView(target), 0)
	//: surface element-walk failure verbatim.
	if derr != nil {
		//: already wrapped.
		return true, derr
	}
	//: typed path expects an exact buffer.
	if len(residual) != 0 {
		//: trailing bytes sentinel.
		return true, trailingBytesError(len(residual))
	}
	//: typed-path success.
	return true, nil
}

// decodeSliceOfStructInto consumes `length` struct records from rest
// and builds a typed []ElemStruct in place on target. Pre-allocates
// the slice with the wire-declared length (clamped against
// sliceHintCap so a malformed declared-length cannot trigger a
// multi-GB allocation).
func decodeSliceOfStructInto(length uint64, rest []byte, targetView reflectView, depth int) (residual []byte, err error) {
	//: unwrap once for method dispatch (Type/Elem/Set).
	target := reflect.Value(targetView)
	//: depth guard — one extra level for the slice's element step.
	if depth+1 > maxTLVDepth {
		//: surface the documented depth sentinel.
		return rest, depthExceededError(depth + 1)
	}
	//: snapshot the element type once.
	elemType := target.Type().Elem()
	//: pre-allocate via the clamped hint to defuse declared-length DoS.
	out := reflect.MakeSlice(target.Type(), 0, sliceHint(length))
	//: walk every element on the wire.
	for range length {
		//: each element MUST carry at least a tag + length header.
		if len(rest) < minRecordBytes {
			//: short buffer — propagate the truncation sentinel.
			return rest, truncatedError()
		}
		//: dispatch on element tag: tagStruct keeps the typed path,
		//: everything else falls back through the generic walker.
		next, elemValue, eerr := decodeSliceElement(rest, elemType, depth+1)
		//: surface per-element decode failure verbatim.
		if eerr != nil {
			//: already wrapped.
			return rest, eerr
		}
		//: append the populated (or zero) element onto the result slice.
		out = reflect.Append(out, elemValue)
		//: advance past the consumed element bytes.
		rest = next
	}
	//: publish through the caller's slice pointer.
	target.Set(out)
	//: walked every element.
	return rest, nil
}

// decodeSliceElement consumes one element record from rest and
// returns the residual bytes + a settable reflect.Value of elemType
// populated with the decoded element. Hoisted out of
// decodeSliceOfStructInto to keep that helper under the linter's
// MAXLOC + CYCLO budgets and to make per-element behaviour testable
// in isolation.
func decodeSliceElement(rest []byte, elemType reflect.Type, depth int) (next []byte, elemValue reflect.Value, err error) {
	//: tagStruct → typed path, byte-skip + struct walker.
	if Tag(rest[0]) == tagStruct {
		//: build a settable element placeholder.
		ev := reflect.New(elemType).Elem()
		//: dispatch into the struct walker (skips the tag byte).
		next2, ferr := decodeStructFromHeader(rest[1:], ev, depth)
		//: surface element-decode failure verbatim.
		if ferr != nil {
			//: already wrapped.
			return rest, reflect.Value{}, ferr
		}
		//: caller appends the populated element.
		return next2, ev, nil
	}
	//: untyped fallback — element isn't a struct record on the wire
	//: (e.g. nil element). Decode via the generic walker and convert
	//: into the typed element via the shared narrowing path.
	val, next3, verr := decodeValue(rest, depth)
	//: surface decode failure verbatim.
	if verr != nil {
		//: already wrapped.
		return rest, reflect.Value{}, verr
	}
	//: build a settable element placeholder (zero value of elemType).
	ev := reflect.New(elemType).Elem()
	//: nil source → element stays at its zero value.
	if val == nil {
		//: caller appends the zero element.
		return next3, ev, nil
	}
	//: route through the shared narrowing path.
	conv, cerr := convertValue(val, elemType)
	//: surface conversion failure verbatim.
	if cerr != nil {
		//: already wrapped.
		return rest, reflect.Value{}, cerr
	}
	//: publish the converted value into the placeholder.
	ev.Set(conv)
	//: caller appends the converted element.
	return next3, ev, nil
}

// decodeStructFromHeader reads the LEB128 field-count header at rest
// and dispatches into decodeStructInto. Hoisted out of
// tryDecodeRootInto so that function stays under the cyclomatic
// budget. depth is threaded through so nested struct fields (Phase 3
// typed recursion) keep the depth guard consistent across the tree.
func decodeStructFromHeader(rest []byte, target reflect.Value, depth int) (residual []byte, err error) {
	//: depth guard — fires before any allocation.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return rest, depthExceededError(depth)
	}
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
	return decodeStructInto(length, rest, target, depth)
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
//
// Phase 3 typed recursion: when the destination field is itself a
// struct AND the wire carries a tagStruct record, recurse into
// decodeStructFromHeader instead of building map[string]any and
// converting. Same shape for slice-of-struct fields. Other field
// types stay on the untyped path so cross-shape narrowings keep
// working without code duplication.
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
	//: Phase 3 typed recursion when field kind + wire tag align.
	if next2, took, terr := tryTypedFieldRecursion(rest, reflectView(target), info, fieldIdx, depth); took {
		//: typed path consumed the value; surface its error verbatim.
		return next2, terr
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

// tryTypedFieldRecursion attempts the Phase 3 typed recursion for a
// struct field whose kind matches the wire tag. Returns took=true when
// the typed path handled the field (along with the advanced cursor
// and any error); took=false signals the caller to fall back to the
// untyped decodeValue + convertValue path.
func tryTypedFieldRecursion(rest []byte, targetView reflectView, info *structTypeInfo, fieldIdx, depth int) (next []byte, took bool, err error) {
	//: unwrap once for Field() dispatch.
	target := reflect.Value(targetView)
	//: short buffer can't hold a tag — leave the untyped path to
	//: produce the truncation diagnostic.
	if len(rest) < minRecordBytes {
		//: signal fall-back; untyped path will report truncation.
		return nil, false, nil
	}
	//: snapshot the field metadata once.
	field := info.fields[fieldIdx]
	//: addressable destination for direct writes.
	dst := target.Field(field.index)
	//: nested-struct typed recursion when both sides agree on shape.
	if field.kind == reflect.Struct && Tag(rest[0]) == tagStruct {
		//: walk the nested struct directly into the field.
		next2, derr := decodeStructFromHeader(rest[1:], dst, depth+1)
		//: caller surfaces err verbatim.
		return next2, true, derr
	}
	//: nested slice-of-struct typed recursion when shapes align.
	if isSliceOfStruct(field.typ) && Tag(rest[0]) == tagSlice {
		//: walk every element directly into the field slice.
		next2, serr := decodeNestedSliceOfStruct(rest[1:], dst, depth+1)
		//: caller surfaces err verbatim.
		return next2, true, serr
	}
	//: nested map typed recursion (Phase 5) when both sides agree.
	if field.kind == reflect.Map && Tag(rest[0]) == tagMap {
		//: walk every (key, value) pair directly into the field map.
		next2, merr := decodeNestedMap(rest[1:], dst, depth+1)
		//: caller surfaces err verbatim.
		return next2, true, merr
	}
	//: shape doesn't match any typed path — caller falls back.
	return nil, false, nil
}

// decodeNestedMap reads the LEB128 pair-count header at rest and
// dispatches into decodeMapInto so a struct field whose value is
// a map[K]V walks the typed map path instead of the untyped
// projector. Phase-5 nested-field recursion companion to
// decodeNestedSliceOfStruct.
func decodeNestedMap(rest []byte, target reflect.Value, depth int) (residual []byte, err error) {
	//: depth guard — Phase 5 paths share the same maxTLVDepth gate.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return rest, depthExceededError(depth)
	}
	//: read the pair count.
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure verbatim.
	if lerr != nil {
		//: already wrapped.
		return rest, lerr
	}
	//: cap-discard structural length before walking pairs.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return rest, sizeExceededError(length)
	}
	//: walk each pair directly into the destination map.
	return decodeMapInto(length, rest, reflectView(target), depth)
}

// isSliceOfStruct reports whether t is a slice whose element type is
// itself a struct. Used by Phase 3 to gate the nested typed-slice
// recursion.
func isSliceOfStruct(t reflect.Type) bool {
	//: slice kind first; non-slice short-circuits cheaply.
	if t.Kind() != reflect.Slice {
		//: not a slice — caller falls back.
		return false
	}
	//: element-kind check — only struct elements take Phase 3.
	return t.Elem().Kind() == reflect.Struct
}

// decodeNestedSliceOfStruct reads the LEB128 element count at rest
// and dispatches into decodeSliceOfStructInto so a struct field whose
// value is a []ElemStruct walks the typed slice path instead of the
// untyped projector.
func decodeNestedSliceOfStruct(rest []byte, target reflect.Value, depth int) (residual []byte, err error) {
	//: depth guard — Phase 3 paths share the same maxTLVDepth gate.
	if depth > maxTLVDepth {
		//: surface the documented depth sentinel.
		return rest, depthExceededError(depth)
	}
	//: read the element count.
	length, rest, lerr := readVarintFromBytes(rest)
	//: surface varint failure verbatim.
	if lerr != nil {
		//: already wrapped.
		return rest, lerr
	}
	//: cap-discard structural length before walking elements.
	if length > uint64(maxTLVBytes) {
		//: surface the size sentinel.
		return rest, sizeExceededError(length)
	}
	//: walk each element directly into the slice.
	return decodeSliceOfStructInto(length, rest, reflectView(target), depth)
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
