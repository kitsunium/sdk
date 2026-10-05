package id

// String implements fmt.Stringer and returns the raw identifier.
func (s Scheme) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(s)
}

// Known reports whether s has been registered in the Generator registry.
func (s Scheme) Known() bool {
	//: empty Schemes are reserved as the invalid zero value.
	if s == "" {
		//: nothing can match the empty Scheme.
		return false
	}
	//: delegate to the registry for the actual lookup.
	_, found := Lookup(s)
	//: propagate the registry's verdict.
	return found
}

// New generates a fresh identifier using the Generator registered under scheme.
// A scheme with no registered generator returns UnknownScheme (blank-import the
// scheme's package to register it); a generator entropy/clock fault propagates
// unchanged with the scheme's own service-layer code.
func New(scheme Scheme) (newID string, err error) {
	//: resolve the generator first so a missing import surfaces a clear sentinel.
	generator, ok := Lookup(scheme)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing scheme.
		return "", UnknownScheme
	}
	//: delegate generation; the scheme owns its entropy / clock source.
	return generator.New()
}
