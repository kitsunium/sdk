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
