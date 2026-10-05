package writer

// String implements fmt.Stringer and returns the raw identifier.
func (n Name) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(n)
}

// Known reports whether n has been registered in the writer registry.
func (n Name) Known() bool {
	//: empty Names are reserved as the invalid zero value.
	if n == "" {
		//: nothing can match the empty Name.
		return false
	}
	//: delegate to the registry for the actual lookup.
	_, found := Lookup(n)
	//: propagate the registry's verdict.
	return found
}

// Config is the opaque per-writer configuration payload. Each Factory
// type-asserts it to its concrete config (e.g. logger.FileConfig); a wrong
// concrete type MUST be rejected with the shared WriterConfigInvalid sentinel
// rather than panic.
type Config = any
