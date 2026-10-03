// Package token — the JWT issuing policy.
package token

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// IssuerConfig configures a JWT issuer.
//
// The zero value is usable and safe: it stamps "iat", requires the caller to
// supply an expiry, and refuses to mint a token that would never expire.
type IssuerConfig struct {
	// Issuer, when non-empty, is stamped as "iss" on claims that carry none.
	Issuer string
	// Type overrides the JWS "typ" header. Empty means "JWT" (RFC 7519 §5.1).
	// Set it to an explicit media type such as "at+jwt" when this issuer mints
	// more than one kind of token (RFC 8725 §3.11 / §3.12).
	Type string
	// KeyID, when non-empty, is stamped as the JWS "kid" header — the value a
	// recipient's key-set verifier selects on. An issuer that publishes a JWK
	// Set needs one; an issuer whose verifier holds a single key does not.
	KeyID string
	// Lifetime, when positive, is stamped as "exp" = now + Lifetime on claims
	// that carry no expiry of their own. Zero means the claims must bring one.
	Lifetime time.Duration
	// AllowMissingExpiry permits minting a token with no "exp" at all. The
	// zero value is false, so the dangerous shape is the one you have to ask
	// for by name: a bearer token that never expires is a password with worse
	// rotation (ADR 0030).
	AllowMissingExpiry bool
	// Clock is the time source for "iat" and the derived "exp". Nil means
	// clock.System. It is a clock.Clock — the frozen read-only half of the
	// time port (ADR 0039) — because issuing stamps instants and never waits.
	Clock clock.Clock
}
