package trace

// normalized is LinkValue.Normalized's body: decl_gen.go writes LinkValue.Normalized, from the
// design, as one call of it.
func (l LinkValue) normalized() LinkValue {
	//: SortAttrs panics on an unusable set, at the call site that wrote it.
	return LinkValue{Context: l.Context, Attrs: SortAttrs(l.Attrs)}
}

// IsValid reports whether the link names a joinable span.
func (l LinkValue) IsValid() bool {
	//: a link is exactly as valid as the context it carries.
	return l.Context.IsValid()
}
