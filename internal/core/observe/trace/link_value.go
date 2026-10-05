package trace

// Normalized returns the link a span actually records: attributes sorted,
// validated and owned.
func (l LinkValue) Normalized() LinkValue {
	//: SortAttrs panics on an unusable set, at the call site that wrote it.
	return LinkValue{Context: l.Context, Attrs: SortAttrs(l.Attrs)}
}

// IsValid reports whether the link names a joinable span.
func (l LinkValue) IsValid() bool {
	//: a link is exactly as valid as the context it carries.
	return l.Context.IsValid()
}
