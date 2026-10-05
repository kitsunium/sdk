package trace

// traceStateEntry is one `key=value` list member. It is unexported because the
// only ways to obtain one are Insert and StateBuilder.Add, both of which
// validate — a struct literal would let an unspellable entry into a header.
type traceStateEntry struct {
	// key is the vendor identifier, already validated against the grammar.
	key string
	// value is the vendor's opaque state, already validated.
	value string
}
