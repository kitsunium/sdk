package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// KeyValue is common.v1.KeyValue: key (1) + value (2). One attribute of a
// resource, a data point, a span, an event or a link.
//
// Field ORDER inside every message of this package is the schema's
// FIELD-NUMBER order, not a reading order: encoding/json emits struct fields as
// declared, and deriving the order from the document is what makes the
// expected bytes in each signal's tests checkable against the .proto field by
// field.
type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

// Attrs renders an attribute set as the repeated KeyValue every OTLP message
// spells its dimensions with. An empty set stays nil, which encoding/json
// omits — the proto3 rule for an empty repeated field.
func Attrs(attrs []coreotel.AttrValue) []KeyValue {
	//: the dimensionless case has no attributes array at all.
	if len(attrs) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized; the set is already sorted by Key.
	pairs := make([]KeyValue, len(attrs))
	//: one KeyValue per dimension, in the payload's canonical order.
	for i, attr := range attrs {
		//: the AnyValue oneof carries the TYPE OTLP has and Prometheus lacks.
		pairs[i] = KeyValueOf(attr)
	}
	//: hand back the rendered set.
	return pairs
}

// KeyValueOf maps one typed attribute onto a KeyValue whose AnyValue names the
// attribute's kind.
//
// Exactly one AnyValue field is non-nil, and it is emitted even when it holds
// the type's zero: a oneof member has explicit presence, so an absent field
// means "no case selected", not "the default". Bool("cache.hit", false) must
// therefore encode as {"boolValue":false} and not as {}.
func KeyValueOf(attr coreotel.AttrValue) KeyValue {
	//: one oneof case per attribute kind.
	switch attr.Kind() {
	//: the common dimension.
	case coreotel.AttrKindString:
		//: stringValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{StringValue: new(attr.Str())}}
	//: a flag.
	case coreotel.AttrKindBool:
		//: boolValue — false is a value, not an absence.
		return KeyValue{Key: attr.Key, Value: AnyValue{BoolValue: new(attr.Bool())}}
	//: a signed 64-bit integer, which rides as a decimal string.
	case coreotel.AttrKindInt64:
		//: intValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{IntValue: new(Int64(attr.Int64()))}}
	//: an IEEE-754 double, non-finite values included.
	case coreotel.AttrKindFloat64:
		//: doubleValue.
		return KeyValue{Key: attr.Key, Value: AnyValue{DoubleValue: new(Double(attr.Float64()))}}
	//: AttrKindInvalid never reaches a payload — every signal refuses it at the
	//: call site that wrote it.
	default:
		//: an empty AnyValue selects no case, which is what "no value" is.
		return KeyValue{Key: attr.Key}
	}
}
