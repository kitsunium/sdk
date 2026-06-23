//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/id .

// Package id is the public facade for SDK identifier generation. One verb,
// four schemes: [New] dispatches by [Scheme] string, and the named helpers
// [UUIDv4]/[UUIDv7]/[ULID]/[Snowflake] return the canonical string form
// directly. UUIDv7 and ULID are time-ordered (k-sortable); UUIDv4 is fully
// random; snowflake packs a 41-bit timestamp + 10-bit node + 12-bit sequence.
//
//	uid, _ := id.UUIDv7()        // "0190b3c0-...-...."
//	sortable, _ := id.ULID()     // "01J8...": lexically time-sortable
//	sf := id.NewSnowflake(7)     // explicit node id
//	s, _ := sf.New()
//
// Activation is automatic: importing this package registers every scheme.
package id

import (
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
)

var (
	// UnknownScheme is returned by New when no generator is registered under the
	// requested Scheme. Matchable via errs.HasReason(err, "UNKNOWN_SCHEME").
	UnknownScheme = coreid.UnknownScheme
	// EntropyFailed wraps a crypto/rand failure while drawing id bytes.
	EntropyFailed = svcid.EntropyFailed
	// ClockBackwards is returned when a snowflake observes a backwards clock.
	ClockBackwards = svcid.ClockBackwards
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

// NewSnowflake returns a snowflake Generator bound to an explicit node id
// (reduced into the 10-bit node space). Not added to the global registry.
func NewSnowflake(node int64) Generator {
	//: hand back a fresh per-node stateful generator.
	return svcid.NewSnowflake(node)
}

// Available returns the sorted list of registered Schemes.
func Available() []Scheme {
	//: delegate to the core registry's sorted key list.
	return coreid.Available()
}
