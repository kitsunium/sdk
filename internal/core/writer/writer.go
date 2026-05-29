// Package writer declares the transport-factory port: a named, config-driven
// constructor that yields a logger Sink, plus the process-wide registry that
// maps a writer Name to its Factory. It is the peer of internal/core/codec —
// the registry resolves a Name to a Factory exactly as codec resolves a Format
// to a Codec (ADR 0012).
//
// A writer is NOT a transport: it builds one. Factory.Open is called once at
// logger-construction time and returns a core/logger.Sink that owns its
// transport for its lifetime; the hot path (Sink.Write) is untouched by this
// package. Concrete factories live in internal/service/writer/<x>/ (console,
// file) and third-party/aws/writer/<x>/ (s3, cloudwatch) and self-register via a
// package-level var initialiser when imported — no init().
package writer

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// Name is the typed key under which a Factory registers (e.g. "console",
// "file", "s3"). The zero value Name("") is reserved invalid, mirroring
// codec.Format.
type Name string

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

// Factory builds a Sink from a Config. It is the swap-able contract the
// registry stores and hands back; concrete factory types stay unexported in
// their own packages.
//
// IFACE-PLUGIN: the registry hands plug-in factory instances back to callers
// so each writer can keep its concrete type unexported; the only stable
// contract is the Factory interface itself.
type Factory interface {
	// Name reports the canonical key under which this factory registers.
	Name() Name
	// Open validates cfg and returns a ready Sink, or a typed error.
	Open(cfg Config) (sink corelogger.Sink, err error)
}
