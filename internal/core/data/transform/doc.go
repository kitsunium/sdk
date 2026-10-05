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
//
// Package transform — holds the process-wide Compressor registry. Service-level
// scheme packages register themselves via package-level var initialisers when
// imported (no init()), mirroring core/data/codec and core/crypto.
//
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
