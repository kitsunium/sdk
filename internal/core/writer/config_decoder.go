// Package writer — declares the optional config Decoder extension that a
// Factory MAY implement to translate a raw, parsed config map into its typed
// Config. It mirrors codec's Appender optional-extension convention: consumers
// detect support with a runtime type assertion and fall back to a default
// mapping that passes the raw map straight through when the extension is absent.
//
// A Factory that parses credentials or other sensitive material out of the raw
// map MUST NOT echo any option value into an error it returns; callers that
// surface a Decoder failure name only the writer and the failure kind.
package writer

// Decoder is the optional extension implemented by a Factory that can build its
// typed Config from a raw map[string]any decoded from a config file. It does NOT
// embed Factory: it is a standalone single-method interface, asserted on a value
// already known to be a Factory. Topology builders (pkg/v1/logger.FromConfig)
// first resolve a Factory from the registry, then type-assert that resolved
// Factory to Decoder: a Factory that also implements Decoder owns the translation
// of its own option keys; one that does not falls back to a default mapping that
// hands the raw map straight to Factory.Open.
//
// It is the writer peer of codec's Appender: a single-method, type-asserted
// optional extension over the base Factory contract.
//
// Implementations MUST:
//   - Translate the recognised keys of raw into the factory's concrete Config
//     type (the same type Factory.Open type-asserts).
//   - Return a typed error on a malformed map; the error MUST NOT include any
//     credential or option value parsed out of raw — name only the failure
//     kind, never the secret material.
//   - Be safe for concurrent use by multiple goroutines.
type Decoder interface {
	// Decode translates raw into a factory's typed Config, or returns a typed
	// error that omits every value parsed out of raw.
	Decode(raw map[string]any) (cfg Config, err error)
}
