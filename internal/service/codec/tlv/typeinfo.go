// Package tlv — type metadata cache shared by encoder and decoder.
//
// Reflection-driven codecs pay reflect.Type.NumField + reflect.Type.Field
// on every encode and every decode. For hot-path payloads with repeated
// types those calls dominate the ns/op budget. structTypeInfoCache
// memoises the (field name, index, type, kind) tuples per reflect.Type
// pointer so subsequent encodes/decodes of the same struct skip the
// reflect walk entirely and reuse the cached metadata.
//
// The cache is global, write-once-per-type, lock-free on the read path
// (sync.Map's read-mostly fast path). reflect.Type pointers are stable
// for the life of the process, so cache invalidation is unnecessary.
package tlv

import (
	"reflect"
	"sync"
)

// structFieldInfo carries the resolved metadata for one exported struct
// field. Pre-resolving the field name as a string saves an allocation
// per Marshal call (reflect.StructField.Name returns a fresh string at
// every Field(i) call on the reflect.Type) and pre-extracting kind
// trims one reflect dispatch from the encode/decode loops.
type structFieldInfo struct {
	//: name is the exported field name as encoded on the TLV wire.
	name string
	//: index is the field position passed to reflect.Value.Field(i).
	index int
	//: typ is the field's declared type, cached for the decoder's
	//: typed-target projection so it doesn't re-query target.Field(i).Type.
	typ reflect.Type
	//: kind is typ.Kind(), cached separately because it's the dispatch
	//: discriminator in the hottest inner loops.
	kind reflect.Kind
}

// structTypeInfo bundles the per-struct cache entry.
type structTypeInfo struct {
	//: fields lists only the EXPORTED fields, in declaration order.
	//: Unexported fields are excluded at build time so the hot loops
	//: don't re-check PkgPath per call.
	fields []structFieldInfo
}

// structTypeInfoCache memoises *structTypeInfo by reflect.Type. The
// cache is process-wide; reflect.Type identities are stable for the
// program lifetime so no eviction is needed.
var structTypeInfoCache sync.Map //: map[reflect.Type]*structTypeInfo

// cachedStructTypeInfo returns the memoised *structTypeInfo for t,
// building it via reflect on first observation. t must have Kind ==
// reflect.Struct — callers are expected to gate the call.
func cachedStructTypeInfo(t reflect.Type) *structTypeInfo {
	//: fast-path: cache hit returns the cached pointer directly.
	if v, ok := structTypeInfoCache.Load(t); ok {
		//: comma-ok guards an unexpected non-*structTypeInfo entry.
		ti, okType := v.(*structTypeInfo)
		//: pool invariant guard — never expected to fail at runtime.
		if !okType {
			//: invariant broken — fail loud at the call site.
			panic("service/codec/tlv: structTypeInfoCache yielded non-*structTypeInfo")
		}
		//: cache hit.
		return ti
	}
	//: slow path: build the metadata once and store.
	ti := buildStructTypeInfo(t)
	//: LoadOrStore prevents duplicate work under concurrent first-call.
	actual, _ := structTypeInfoCache.LoadOrStore(t, ti)
	//: comma-ok guards the same invariant.
	out, okType := actual.(*structTypeInfo)
	//: pool invariant guard.
	if !okType {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/tlv: structTypeInfoCache yielded non-*structTypeInfo on LoadOrStore")
	}
	//: actual is *structTypeInfo by construction.
	return out
}

// buildStructTypeInfo walks t's fields once and returns a structTypeInfo
// containing only the exported ones. Hoisted out of cachedStructTypeInfo
// to keep that helper under the KTN-FUNC-MAXLOC budget.
func buildStructTypeInfo(t reflect.Type) *structTypeInfo {
	//: pre-size to the worst case (every field exported).
	n := t.NumField()
	fields := make([]structFieldInfo, 0, n)
	//: walk in declaration order to match the TLV wire layout.
	for i := range n {
		//: snapshot the field metadata.
		sf := t.Field(i)
		//: skip unexported names — PkgPath is empty for exported.
		if sf.PkgPath != "" {
			//: not exported; the encoder and decoder must agree to skip it.
			continue
		}
		//: capture name + index + typ + kind for the hot loops.
		fields = append(fields, structFieldInfo{
			name:  sf.Name,
			index: i,
			typ:   sf.Type,
			kind:  sf.Type.Kind(),
		})
	}
	//: caller stores by reflect.Type pointer.
	return &structTypeInfo{fields: fields}
}
