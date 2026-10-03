// Package metrics — this signal's half of the shared attribute model
// (internal/core/otel): the refusal an unusable set earns HERE, the Resource a
// Meter publishes, and the one attribute key only a meter ever writes.
package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/otel"

// OverflowAttrKey names the reserved attribute the meter attaches to the single
// aggregated series it folds observations into once an instrument has reached
// its cardinality bound. It is spelled with underscores rather than dots so it
// needs no mangling to be a legal Prometheus label name — unlike every
// OTel-conventional key, which is dotted.
//
// The key is RESERVED: a caller who passes it explicitly with a BOOL value is
// writing into the same series the meter uses for overflow, and their
// observations merge with it. That is not enforced — enforcing it would cost a
// comparison per attribute on the lookup path to prevent a collision nobody
// reaches by accident.
const OverflowAttrKey string = "sdk_metric_overflow"

// ValidateAttrs panics with InvalidAttribute (0.2.9.4) when sorted cannot name
// a series: an empty Key, a value no constructor ever set, or the same Key
// twice. It is the check every instrument fetch runs, on the already sorted
// set; the rules are the shared model's (internal/core/otel.ValidateAttrs), the
// code is this signal's.
func ValidateAttrs(sorted []coreotel.AttrValue) {
	//: the shared rules, refused under this signal's own code.
	coreotel.ValidateAttrs(sorted, InvalidAttribute)
}

// NormalizeResource returns the ResourceValue a Meter publishes: attributes
// sorted, validated, owned, and carrying ServiceNameKey whether or not the
// caller supplied it. An unusable attribute set panics with InvalidAttribute,
// at construction, rather than at the first export.
func NormalizeResource(resource coreotel.ResourceValue) coreotel.ResourceValue {
	//: the shared normalisation, refused under this signal's own code.
	return coreotel.NormalizeResource(resource, InvalidAttribute)
}
