// Package logger: attr.go defines the AttrValue type — an immutable key/value
// pair attached to a RecordEvent. The Value field is now a kind-discriminated
// union (see value.go) instead of an open `any`, so handlers dispatch on the
// Kind enum and pay no boxing cost on the hot path.
package logger

// AttrValue is an immutable key/value pair carried by a RecordEvent. Use the
// typed Value constructors (StringValue, Int64Value, …) to build the Value
// field — the zero Value is a valid KindAny carrying nil.
type AttrValue struct {
	// Key identifies the attribute in the output line.
	Key string
	// Value carries the typed payload; handlers dispatch on Value.Kind().
	Value Value
}
