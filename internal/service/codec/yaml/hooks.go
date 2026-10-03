// Package yaml — the hooks a Go type implements, read once per type.
package yaml

import (
	"encoding"
	"reflect"
	"sync"
	"time"
)

// The hooks a type may implement, one bit of typeHooks each.
const (
	// hookMarshaler: T implements marshalYAMLer.
	hookMarshaler typeHooks = 1 << iota
	// hookPtrMarshaler: *T implements marshalYAMLer and T does not.
	hookPtrMarshaler
	// hookTextMarshaler: T implements encoding.TextMarshaler.
	hookTextMarshaler
	// hookPtrTextMarshaler: *T implements encoding.TextMarshaler and T does not.
	hookPtrTextMarshaler
	// hookUnmarshaler: *T implements unmarshalYAMLer.
	hookUnmarshaler
	// hookForeignUnmarshaler: *T has an UnmarshalYAML method of another
	// signature — yaml.v3's UnmarshalYAML(*yaml.Node) — which the decoder
	// cannot call.
	hookForeignUnmarshaler
	// hookTextUnmarshaler: *T implements encoding.TextUnmarshaler.
	hookTextUnmarshaler
	// hookIsZeroer: T implements IsZero() bool.
	hookIsZeroer
)

// The hook interfaces as reflected types, the types the codec treats as
// text, and the cache: reflection runs once per type for the life of the
// process.
var (
	marshalerType       = reflect.TypeFor[marshalYAMLer]()
	unmarshalerType     = reflect.TypeFor[unmarshalYAMLer]()
	textMarshalerType   = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	isZeroerType        = reflect.TypeFor[isZeroer]()
	durationType        = reflect.TypeFor[time.Duration]()
	timeType            = reflect.TypeFor[time.Time]()

	// hooksCache maps a reflect.Type to its typeHooks.
	hooksCache sync.Map
)

// typeHooks records which hooks a type and its pointer implement, one bit
// each.
type typeHooks uint16

// has reports whether the hook bit is set.
func (h typeHooks) has(hook typeHooks) bool {
	//: the bit.
	return h&hook != 0
}

// hooksOf returns the hooks t implements, from the cache.
func hooksOf(t reflect.Type) typeHooks {
	//: known.
	if cached, ok := hooksCache.Load(t); ok {
		//: cached; a value of another type would be a cache of something else.
		if hooks, isHooks := cached.(typeHooks); isHooks {
			return hooks
		}
	}
	hooks := computeHooks(t)
	hooksCache.Store(t, hooks)
	//: computed.
	return hooks
}

// computeHooks reads by reflection which hooks t and its pointer implement.
func computeHooks(t reflect.Type) typeHooks {
	ptr := reflect.PointerTo(t)
	var h typeHooks
	//: each hook a type or its pointer implements, as a bit.
	for _, check := range []struct {
		target reflect.Type
		iface  reflect.Type
		bit    typeHooks
	}{
		{t, marshalerType, hookMarshaler},
		{t, textMarshalerType, hookTextMarshaler},
		{ptr, unmarshalerType, hookUnmarshaler},
		{ptr, textUnmarshalerType, hookTextUnmarshaler},
		{t, isZeroerType, hookIsZeroer},
	} {
		//: implemented.
		if check.target.Implements(check.iface) {
			h |= check.bit
		}
	}
	//: the hooks only the pointer has, and a foreign UnmarshalYAML.
	return h | pointerOnlyHooks(ptr, h)
}

// pointerOnlyHooks returns the bits for the marshal hooks only ptr — not the
// type it points to — implements, and for an UnmarshalYAML whose signature
// the decoder cannot call.
func pointerOnlyHooks(ptr reflect.Type, h typeHooks) typeHooks {
	var extra typeHooks
	//: MarshalYAML only on the pointer.
	if !h.has(hookMarshaler) && ptr.Implements(marshalerType) {
		extra |= hookPtrMarshaler
	}
	//: MarshalText only on the pointer.
	if !h.has(hookTextMarshaler) && ptr.Implements(textMarshalerType) {
		extra |= hookPtrTextMarshaler
	}
	//: an UnmarshalYAML of another signature.
	if _, has := ptr.MethodByName("UnmarshalYAML"); has && !h.has(hookUnmarshaler) {
		extra |= hookForeignUnmarshaler
	}
	//: the extra bits.
	return extra
}
