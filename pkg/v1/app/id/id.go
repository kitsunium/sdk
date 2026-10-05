//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/id .

// Package id is the public facade for SDK identifier generation. One verb,
// seven schemes: [New] dispatches by [Scheme] string, and the named helpers
// [UUIDv4]/[UUIDv7]/[ULID]/[Snowflake]/[NanoID]/[KSUID] return the canonical
// string form directly.
//
// Pick by what you need the identifier to carry:
//
//   - [UUIDv4] — 122 random bits, unguessable, unordered.
//
//   - [UUIDv7] and [ULID] — a millisecond prefix makes them k-sortable; ULID
//     renders in 26 Crockford base32 characters instead of 36.
//
//   - [Snowflake] — 41-bit timestamp + 10-bit node + 12-bit sequence, for
//     coordinated per-node issuance.
//
//   - [NanoID] — 21 URL-safe characters, the shortest of the set, no timestamp.
//
//   - [KSUID] — 27 base62 characters, sortable to the second, and decodable:
//     [ParseKSUID] recovers when an identifier was issued.
//
//   - TypeID — a type prefix plus a UUIDv7, "user_01h2xcejqtf2nbrexx3vqjhp41".
//     An identifier that names its own entity cannot be pasted into the wrong
//     column unnoticed. Built with [NewTypeID], read with [ParseTypeID], and
//     [FormatTypeID] relabels UUIDs a caller already stores.
//
// In practice:
//
//	uid, _ := id.UUIDv7()        // "0190b3c0-...-...."
//	sortable, _ := id.ULID()     // "01J8...": lexically time-sortable
//	short, _ := id.NanoID()      // "V1StGXR8_Z5jdHi6B-myT"
//	sf := id.NewSnowflake(7)     // explicit node id
//	s, _ := sf.New()
//	users, _ := id.NewTypeID("user")
//	u, _ := users.New()          // "user_01h2xcejqtf2nbrexx3vqjhp41"
//
// Activation is automatic: importing this package registers every scheme that
// needs no configuration. NanoID and TypeID also take explicit constructors,
// and those REFUSE a length of zero or an invalid type prefix rather than hand
// back a generator that mints degraded identifiers (ADR 0031).
package id

import (
	svcid "github.com/kitsunium/sdk/internal/service/app/id"
)

const (
	// UUIDv4Scheme is the random-UUID scheme key.
	UUIDv4Scheme Scheme = "uuidv4"
	// UUIDv7Scheme is the time-ordered UUID scheme key.
	UUIDv7Scheme Scheme = "uuidv7"
	// ULIDScheme is the ULID scheme key.
	ULIDScheme Scheme = "ulid"
	// SnowflakeScheme is the snowflake scheme key.
	SnowflakeScheme Scheme = "snowflake"
	// NanoIDScheme is the NanoID scheme key.
	NanoIDScheme Scheme = "nanoid"
	// KSUIDScheme is the KSUID scheme key.
	KSUIDScheme Scheme = "ksuid"
	// TypeIDScheme is the TypeID scheme key. It is the only scheme key New
	// cannot resolve: a TypeID needs a type prefix, and there is no
	// non-arbitrary prefix the SDK could choose on a caller's behalf, so no
	// generator is registered under it. New(TypeIDScheme) returns
	// UnknownScheme by design — build one with NewTypeID instead. The key is
	// exported because Generator.Scheme() reports it.
	TypeIDScheme Scheme = "typeid"
	// ksuidPayloadBytes is the width of a KSUID's random payload. It names the
	// array length in ParseKSUID's signature; the rendered type is [16]byte
	// either way, and it mirrors the service-layer constant.
	ksuidPayloadBytes int = 16
)

// UUIDv4 returns a fresh random UUIDv4 in canonical dashed-hex form.
func UUIDv4() (newID string, err error) {
	//: the registered singleton avoids a registry lookup.
	return svcid.UUIDv4.New()
}

// UUIDv7 returns a fresh time-ordered UUIDv7 (k-sortable by creation time).
func UUIDv7() (newID string, err error) {
	//: the registered singleton avoids a registry lookup.
	return svcid.UUIDv7.New()
}

// ULID returns a fresh ULID (26-char Crockford base32, lexically time-sortable).
func ULID() (newID string, err error) {
	//: the registered singleton avoids a registry lookup.
	return svcid.ULID.New()
}

// Snowflake returns a fresh id from the default-node snowflake generator.
func Snowflake() (newID string, err error) {
	//: the registered default-node singleton avoids a registry lookup.
	return svcid.Snowflake.New()
}

// NanoID returns a fresh 21-character NanoID over the URL-safe alphabet. The
// characters are drawn by rejection sampling, so every symbol is equally
// likely; 21 of them carry 126 bits, slightly more than a UUIDv4.
func NanoID() (newID string, err error) {
	//: the registered default-size singleton avoids a registry lookup.
	return svcid.NanoID.New()
}

// KSUID returns a fresh KSUID: 27 base62 characters that sort by creation time
// as plain strings. Feed the result to ParseKSUID to recover that time.
func KSUID() (newID string, err error) {
	//: the registered singleton avoids a registry lookup.
	return svcid.KSUID.New()
}
