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
