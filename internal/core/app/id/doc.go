// Package id — ranges 0.2.7.* (ADR 0024 core/app/id block) and 0.3.39.*
// (ADR 0024 service/app/id block, declared here since ADR 0160).
//
// Package id — declares the sentinels of the domain: those the Generator
// facade returns, and those the concrete schemes raise. A registry sentinel's
// name equals its errs.Define Reason in SCREAMING_SNAKE form; a scheme
// sentinel's Reason is its Code constant's name, minus Code, in the same form
// (ADR 0020's namespaced derivation: CodeIDMalformed → ID_MALFORMED).
//
// Package id declares the identifier-generation port of the SDK: the Generator
// contract and the typed Scheme string under which a generator registers. It is
// a core sibling beside codec, writer, crypto, logger, transform, and proc
// (ADR 0024) and mirrors transform exactly — the registry resolves a Scheme to
// a Generator the way transform resolves an Algorithm to a Compressor.
//
// No generation bodies live here; concrete generators (UUIDv4/UUIDv7/ULID/
// snowflake/NanoID/KSUID) live under internal/service/app/id/ and self-register via
// a package-level var initialiser when imported — no init(). TypeID lives there
// too but is NOT registered: it carries a caller-chosen type prefix, and there
// is no prefix the SDK could invent on their behalf, so New("typeid") returns
// UnknownScheme by design. The canonical external form of every identifier is
// its string rendering, so Generator.New returns a string (UUIDs dashed-hex,
// ULID Crockford base32, snowflake decimal, KSUID base62, NanoID URL-safe).
//
// Package id — holds the process-wide Generator registry. Service-level scheme
// packages register themselves via package-level var initialisers when imported
// (no init()), mirroring core/data/codec, core/crypto, and core/data/transform.
package id
