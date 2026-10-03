// Package trace — this signal's half of the shared attribute model
// (internal/core/observe/otel): the refusal an unusable set earns HERE, and the
// Resource a Tracer publishes.
package trace

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// ValidateAttrs panics with InvalidAttribute (0.2.20.7) when sorted cannot name
// a dimension set: an empty Key, a value no constructor ever set, or the same
// Key twice. The rules are the shared model's (internal/core/observe/otel); the code is
// this signal's own.
func ValidateAttrs(sorted []coreotel.AttrValue) {
	//: the shared rules, refused under this signal's own code.
	coreotel.ValidateAttrs(sorted, InvalidAttribute)
}

// SortAttrs returns a sorted, validated, owned copy of attrs — what a span, an
// event and a link record, so the caller's slice cannot be mutated afterwards
// through the span. An unusable set panics with InvalidAttribute, at the call
// site that wrote it rather than at the first export.
func SortAttrs(attrs []coreotel.AttrValue) []coreotel.AttrValue {
	//: the shared sort, refused under this signal's own code.
	return coreotel.SortAttrs(attrs, InvalidAttribute)
}

// NormalizeResource returns the ResourceValue a Tracer publishes: attributes
// sorted, validated, owned, and carrying ServiceNameKey whether or not the
// caller supplied it. An unusable attribute set panics with InvalidAttribute,
// at construction, rather than at the first export.
func NormalizeResource(resource coreotel.ResourceValue) coreotel.ResourceValue {
	//: the shared normalisation, refused under this signal's own code.
	return coreotel.NormalizeResource(resource, InvalidAttribute)
}
