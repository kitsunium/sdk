// Package yaml — the hooks a Go type may implement to choose its YAML.
package yaml

import "reflect"

// marshalYAMLer is the hook a type implements to choose what it is written
// as: MarshalYAML returns the value written in its place. It is
// gopkg.in/yaml.v3's Marshaler, method for method, so a type written for
// yaml.v3 keeps working.
type marshalYAMLer interface {
	MarshalYAML() (any, error)
}

// unmarshalYAMLer is the hook a type implements to read itself:
// UnmarshalYAML is handed a function that decodes the node into any value
// the hook chooses. It is the hook yaml.v2 defined and yaml.v3 still calls.
// yaml.v3's other form, UnmarshalYAML(*yaml.Node), names a yaml.v3 type and
// is refused.
type unmarshalYAMLer interface {
	UnmarshalYAML(unmarshal func(any) error) error
}

// isZeroer is the hook omitempty asks first, as yaml.v3 does: time.Time is
// empty when IsZero says so.
type isZeroer interface {
	IsZero() bool
}

// marshalYAMLHook returns v's MarshalYAML hook, when v has one.
//
// IFACE-PLUGIN: the hook is a method of the caller's own type, which the codec
// knows only through this interface.
func marshalYAMLHook(v reflect.Value) (marshalYAMLer, bool) {
	//: asserted on the reflected value, without boxing it.
	return reflect.TypeAssert[marshalYAMLer](v)
}

// unmarshalYAMLHook returns v's UnmarshalYAML hook, when v has one.
//
// IFACE-PLUGIN: the hook is a method of the caller's own type, which the codec
// knows only through this interface.
func unmarshalYAMLHook(v reflect.Value) (unmarshalYAMLer, bool) {
	//: asserted on the reflected value, without boxing it.
	return reflect.TypeAssert[unmarshalYAMLer](v)
}

// isZeroHook returns v's IsZero hook, when v has one.
//
// IFACE-PLUGIN: the hook is a method of the caller's own type, which the codec
// knows only through this interface.
func isZeroHook(v reflect.Value) (isZeroer, bool) {
	//: asserted on the reflected value, without boxing it.
	return reflect.TypeAssert[isZeroer](v)
}
