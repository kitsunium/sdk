// Package codec — declares the optional Appender extension that
// hot-path encoders (NDJSON, JSON, text) implement so callers can write into
// a caller-supplied buffer without paying for an intermediate allocation.
//
// Codecs that satisfy Codec but do NOT implement Appender remain valid; the
// logger and other consumers detect support with a runtime type assertion
// and fall back to Marshal + copy when the optional interface is absent.
//
// Package codec declares the domain contracts every format codec in the SDK
// implements. Consumers interact with codecs through the pkg/v1/data/codec facade;
// concrete implementations live in internal/service/data/codec/<format>/.
//
// This package is interface-only: no runtime state, no registrations, no
// format-specific knowledge. Codecs register themselves via the registry in
// registry.go when their service subpackage is imported.
package codec
