package codec

// Known reports whether f has been registered in the codec registry.
func (f Format) Known() bool {
	//: empty Formats are reserved as the invalid zero value.
	if f == "" {
		//: nothing can match the empty Format.
		return false
	}
	//: delegate to the registry for the actual lookup.
	_, found := Lookup(f)
	//: propagate the registry's verdict.
	return found
}

// String implements fmt.Stringer and returns the raw identifier.
func (f Format) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(f)
}
