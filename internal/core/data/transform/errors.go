// Package transform — range 0.2.5.* (ADR 0014 core/data/transform block), and
// the stdlib schemes' 0.3.26.* (ADR 0014 service/data/transform block, declared
// here since ADR 0160).
//
// Package transform — declares the sentinels returned by the Compressor
// facade, and the three the stdlib schemes in internal/service/data/transform
// wrap a compress/* failure in (ADR 0160: every code is declared in the core,
// at the service's path). Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. CodeUnknownCompressor is also surfaced via panic at
// boot on a duplicate registration (see registry.go), not only as an *Error
// sentinel.
package transform

// exitDataErr matches sysexits EX_DATAERR — a malformed or undecompressable
// frame is a data problem, not a generic internal software error (70).
const exitDataErr int = 65
