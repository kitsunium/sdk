package trace

// isValid is SpanContextValue.IsValid's body: decl_gen.go writes SpanContextValue.IsValid, from the
// design, as one call of it.
func (c SpanContextValue) isValid() bool {
	//: the two normative validity rules, and nothing else — flags and state
	//: are optional decoration on a context that is already valid or not.
	return c.TraceID.IsValid() && c.SpanID.IsValid()
}

// isSampled is SpanContextValue.IsSampled's body: decl_gen.go writes SpanContextValue.IsSampled, from the
// design, as one call of it.
func (c SpanContextValue) isSampled() bool {
	//: bit 0 of the traceparent flag byte.
	return c.Flags.IsSampled()
}

// withState is SpanContextValue.WithState's body: decl_gen.go writes SpanContextValue.WithState, from the
// design, as one call of it.
func (c SpanContextValue) withState(state StateValue) SpanContextValue {
	//: value receiver: c is already a copy, so this mutates nothing.
	c.State = state
	//: the updated identity.
	return c
}
