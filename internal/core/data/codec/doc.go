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
//
// Package codec — range 0.2.2.* (ADR 0005 core/data/codec block).
//
// Package codec — declares the registry's boot-time sentinel. Its var name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package codec — declares the typed Format string and its helpers.
// The zero Format ("") is invalid — consumers obtain a Format via the
// pkg/v1/data/codec constants or via the FromMIME / FromExtension helpers.
//
// Package codec — holds the process-wide Codec registry.
// Service-level codec packages register themselves via package-level var
// initialisers when the package is imported.
package codec
