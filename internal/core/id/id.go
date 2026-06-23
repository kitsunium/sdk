// Package id declares the identifier-generation port of the SDK: the Generator
// contract and the typed Scheme string under which a generator registers. It is
// a core sibling beside codec, writer, crypto, logger, transform, and proc
// (ADR 0024) and mirrors transform exactly — the registry resolves a Scheme to
// a Generator the way transform resolves an Algorithm to a Compressor.
//
// No generation bodies live here; concrete generators (UUIDv4/UUIDv7/ULID/
// snowflake) live under internal/service/id/ and self-register via a
// package-level var initialiser when imported — no init(). The canonical
// external form of every identifier is its string rendering, so Generator.New
// returns a string (UUIDs dashed-hex, ULID Crockford base32, snowflake decimal).
package id

// Scheme is the typed key under which a Generator registers (e.g. "uuidv4",
// "uuidv7", "ulid", "snowflake"). The zero value Scheme("") is reserved
// invalid, mirroring codec.Format and transform.Algorithm.
type Scheme string

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

// Generator is the contract every identifier scheme satisfies. Instances MUST
// be safe for concurrent use (New is called from many goroutines). Scheme-
// specific options (snowflake node id, etc.) are supplied via the scheme's
// constructor, not via this interface.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind this
// interface — concrete scheme types stay unexported per package.
type Generator interface {
	Scheme() Scheme
	New() (newID string, err error)
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
