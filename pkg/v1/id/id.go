//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/id .

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
	"time"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// Scheme is the public alias for the identifier-scheme key.
type Scheme = coreid.Scheme

// Generator is the public alias for the identifier-generator contract.
type Generator = coreid.Generator

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

var (
	// UnknownScheme is returned by New when no generator is registered under the
	// requested Scheme. Matchable via errs.HasReason(err, "UNKNOWN_SCHEME").
	UnknownScheme = coreid.UnknownScheme
	// EntropyFailed wraps a crypto/rand failure while drawing id bytes.
	EntropyFailed = svcid.EntropyFailed
	// ClockBackwards is returned when a snowflake observes a backwards clock.
	ClockBackwards = svcid.ClockBackwards
	// InvalidSize is returned by NewNanoID for a non-positive identifier length.
	InvalidSize = svcid.InvalidSize
	// InvalidPrefix is returned by NewTypeID, ParseTypeID and FormatTypeID for a
	// type prefix that is empty or outside lowercase ASCII.
	InvalidPrefix = svcid.InvalidPrefix
	// Malformed is returned by ParseKSUID, ParseTypeID and FormatTypeID when the
	// input is not a well-formed rendering for its scheme.
	Malformed = svcid.Malformed
	// TimestampRange is returned when the clock sits outside the window a
	// scheme's timestamp field can represent.
	TimestampRange = svcid.TimestampRange
)

// New generates a fresh identifier using the generator registered under scheme.
func New(scheme Scheme) (newID string, err error) {
	//: delegate to the core dispatch (UnknownScheme on a missing scheme).
	return coreid.New(scheme)
}

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

// NewSnowflake returns a snowflake Generator bound to an explicit node id
// (reduced into the 10-bit node space). Not added to the global registry.
func NewSnowflake(node int64) Generator {
	//: hand back a fresh per-node stateful generator.
	return svcid.NewSnowflake(node)
}

// NewNanoID returns a NanoID Generator producing size characters, and refuses a
// non-positive size rather than return one that mints empty strings. Not added
// to the global registry — the registered NanoID singleton is the 21-character
// default.
func NewNanoID(size int) (g Generator, err error) {
	//: the service constructor owns the refusal.
	return svcid.NewNanoID(size)
}

// NewTypeID returns a TypeID Generator stamping prefix on every identifier —
// "user_01h2xcejqtf2nbrexx3vqjhp41". prefix must be 1 to 63 lowercase ASCII
// letters, with '_' allowed only between two letters; anything else is refused
// here rather than at every call. Not added to the global registry, because no
// prefix would be a defensible default (see TypeIDScheme).
func NewTypeID(prefix string) (g Generator, err error) {
	//: the service constructor owns the prefix rules and the refusal.
	return svcid.NewTypeID(prefix)
}

// ParseKSUID decodes a canonical 27-character KSUID, returning the instant it
// was issued (second resolution, UTC) and its 16-byte random payload.
func ParseKSUID(ksuid string) (issued time.Time, payload [ksuidPayloadBytes]byte, err error) {
	//: the service parser owns the base62 decoding and its refusals.
	return svcid.ParseKSUID(ksuid)
}

// ParseTypeID splits a canonical TypeID into its type prefix and the dashed-hex
// UUID its suffix encodes. It is the exact inverse of FormatTypeID.
func ParseTypeID(typeID string) (prefix, uuid string, err error) {
	//: the service parser owns the split, the prefix rules and the decoding.
	return svcid.ParseTypeID(typeID)
}

// FormatTypeID renders prefix and a canonical dashed-hex UUID as a TypeID. It
// is the migration path: an existing UUID column becomes typed without
// reissuing anything, so the UUID version is not enforced.
func FormatTypeID(prefix, uuid string) (typeID string, err error) {
	//: the service formatter owns the prefix rules and the UUID parsing.
	return svcid.FormatTypeID(prefix, uuid)
}

// Available returns the sorted list of registered Schemes.
func Available() []Scheme {
	//: delegate to the core registry's sorted key list.
	return coreid.Available()
}
