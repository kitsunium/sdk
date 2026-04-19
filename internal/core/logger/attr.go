// Package logger: attr.go defines the AttrValue type — an immutable key/value
// pair attached to a RecordEvent. Handlers inspect Value's concrete type at
// format time so the core interfaces stay allocation-light.
package logger

// AttrValue is a typed key/value pair attached to a RecordEvent; the Value is
// any so handlers can perform type-specific formatting without interface bloat.
type AttrValue struct {
	// Key identifies the attribute in the output line.
	Key string
	// Value is the attribute payload; handlers inspect the concrete type.
	Value any
}
