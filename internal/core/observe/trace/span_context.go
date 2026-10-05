package trace

// IsValid reports whether the context names a joinable span: both identifiers
// present and non-zero.
func (c SpanContextValue) IsValid() bool {
	//: the two normative validity rules, and nothing else — flags and state
	//: are optional decoration on a context that is already valid or not.
	return c.TraceID.IsValid() && c.SpanID.IsValid()
}

// IsSampled reports whether the sampled bit is set on this context.
//
// It is the ONLY sampling question anything downstream asks. The decision is
// taken once, at the root of the trace, and travels in this bit; re-deciding per
// span produces a trace with holes in the middle, which is worse than no trace
// at all because it looks complete.
func (c SpanContextValue) IsSampled() bool {
	//: bit 0 of the traceparent flag byte.
	return c.Flags.IsSampled()
}

// WithState returns a copy of c carrying state. It exists so a caller who
// mutates the vendor list does not have to rebuild the identity around it.
func (c SpanContextValue) WithState(state StateValue) SpanContextValue {
	//: value receiver: c is already a copy, so this mutates nothing.
	c.State = state
	//: the updated identity.
	return c
}
