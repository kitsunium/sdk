// Package transform declares the byte-transform port of the SDK: the
// Compressor contract and the typed Algorithm string under which a compressor
// registers. It is the fifth core sibling beside codec, writer, crypto, and
// logger (ADR 0014 D1) and mirrors codec exactly — the registry resolves an
// Algorithm to a Compressor the way codec resolves a Format to a Codec.
//
// No algorithm bodies and no vendor types live here; concrete compressors live
// under internal/service/data/transform/ (stdlib gzip/flate/zlib today) and
// self-register via a package-level var initialiser when imported — no init().
package transform

// Algorithm is the typed key under which a Compressor registers (e.g. "gzip",
// "flate", "zlib"). The zero value Algorithm("") is reserved invalid, mirroring
// codec.Format and crypto.Algorithm. Note that "flate" is the raw DEFLATE
// stream of RFC 1951 and "zlib" is the RFC 1950 envelope HTTP misnames
// "deflate" — distinct Algorithms, not aliases.
type Algorithm string

// String implements fmt.Stringer and returns the raw identifier.
func (a Algorithm) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(a)
}

// Known reports whether a has been registered in the Compressor registry.
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
