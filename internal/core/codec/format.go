// Package codec: format.go declares the typed Format string and its helpers.
// The zero Format ("") is invalid — consumers obtain a Format via the
// pkg/v1/codec constants or via the FromMIME / FromExtension helpers.
package codec

// Format is the typed string identifier of a codec.
type Format string

// Known reports whether f has been registered in the codec registry.
//
// Returns:
//   - bool: true iff a codec with this Format is registered.
func (f Format) Known() (ok bool) {
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
//
// Returns:
//   - string: the Format value unchanged.
func (f Format) String() (s string) {
	//: direct cast from the typed string back to a plain string.
	return string(f)
}
