// Package id wraps stdlib crypto/rand + the kernel clock as core/app/id.Generator
// implementations (UUIDv4, UUIDv7, ULID, snowflake, NanoID, KSUID, TypeID).
// Blank-importing this package self-registers every scheme that needs no
// configuration; snowflake, NanoID and TypeID also offer explicit constructors.
// No init() — registration is package-level var.
//
// Randomness is read with crypto/rand.Read, which since Go 1.24 never returns
// an error and always fills its buffer — a failing source crashes the program
// instead — so no generator here fails for want of entropy. EntropyFailed stays
// declared, and published, but nothing returns it.
//
// Package id — KSUID generator (32-bit second prefix + 128-bit payload, base62).
//
// Package id — NanoID generator (URL-safe alphabet, configurable length).
//
// Package id — snowflake (Twitter-style) stateful generator.
//
// Package id — TypeID generator (type prefix + UUIDv7 in lowercase base32).
//
// Package id — ULID generator (48-bit time + 80-bit random, Crockford base32).
//
// Package id — UUIDv4 (random) generator (RFC 9562 §5.4).
//
// Package id — UUIDv7 (time-ordered) generator (RFC 9562 §5.7).
package id
