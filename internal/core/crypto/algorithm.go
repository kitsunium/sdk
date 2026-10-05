package crypto

// Algorithm is the typed key under which an AEAD registers (e.g.
// "aes-256-gcm"). The zero value Algorithm("") is reserved invalid, mirroring
// codec.Format.
type Algorithm string

// String implements fmt.Stringer and returns the raw identifier.
func (a Algorithm) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(a)
}

// Known reports whether a has been registered in the AEAD registry.
func (a Algorithm) Known() bool {
	//: empty Algorithms are reserved as the invalid zero value.
	if a == "" {
		//: nothing can match the empty Algorithm.
		return false
	}
	//: delegate to the registry for the actual lookup.
	_, found := Lookup(a)
	//: propagate the registry's verdict.
	return found
}
