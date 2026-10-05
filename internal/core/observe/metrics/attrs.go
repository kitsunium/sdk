package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

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

// validateAttrs is ValidateAttrs's body: decl_gen.go writes ValidateAttrs, from the
// design, as one call of it.
func validateAttrs(sorted []coreotel.AttrValue) {
	//: the shared rules, refused under this signal's own code.
	coreotel.ValidateAttrs(sorted, InvalidAttribute)
}

// normalizeResource is NormalizeResource's body: decl_gen.go writes NormalizeResource, from the
// design, as one call of it.
func normalizeResource(resource coreotel.ResourceValue) coreotel.ResourceValue {
	//: the shared normalisation, refused under this signal's own code.
	return coreotel.NormalizeResource(resource, InvalidAttribute)
}
