package trace

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// validateAttrs is ValidateAttrs's body: decl_gen.go writes ValidateAttrs, from the
// design, as one call of it.
func validateAttrs(sorted []coreotel.AttrValue) {
	//: the shared rules, refused under this signal's own code.
	coreotel.ValidateAttrs(sorted, InvalidAttribute)
}

// sortAttrs is SortAttrs's body: decl_gen.go writes SortAttrs, from the
// design, as one call of it.
func sortAttrs(attrs []coreotel.AttrValue) []coreotel.AttrValue {
	//: the shared sort, refused under this signal's own code.
	return coreotel.SortAttrs(attrs, InvalidAttribute)
}

// normalizeResource is NormalizeResource's body: decl_gen.go writes NormalizeResource, from the
// design, as one call of it.
func normalizeResource(resource coreotel.ResourceValue) coreotel.ResourceValue {
	//: the shared normalisation, refused under this signal's own code.
	return coreotel.NormalizeResource(resource, InvalidAttribute)
}
