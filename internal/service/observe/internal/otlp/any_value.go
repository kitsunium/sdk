package otlp

// AnyValue is common.v1.AnyValue, restricted to the four SCALAR cases the
// SDK's attribute model has: stringValue (1), boolValue (2), intValue (3),
// doubleValue (4).
//
// arrayValue (5), kvlistValue (6) and bytesValue (7) are absent because
// internal/core/observe/otel.AttrValue has no corresponding kind — homogeneous array
// attributes are deferred with a measured reason (ADR 0044 §Decision 2).
// Exactly one pointer is non-nil, and it is emitted even at its zero value,
// because a oneof member has explicit presence.
type AnyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	BoolValue   *bool   `json:"boolValue,omitempty"`
	IntValue    *Int64  `json:"intValue,omitempty"`
	DoubleValue *Double `json:"doubleValue,omitempty"`
}
