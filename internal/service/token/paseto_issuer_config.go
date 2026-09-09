// Package token — the PASETO v4.public issuing policy.
package token

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// PasetoIssuerConfig configures a PASETO v4.public issuer.
//
// It is a flat struct rather than an [IssuerConfig] plus two extras, and the
// missing fields are the point: PASETO has no header parameters, so there is
// nothing for Type or KeyID to set. Embedding the JWS config would have
// published two knobs this format silently ignores, and a configuration field
// that does nothing is a bug waiting for somebody to set it. The equivalent of
// a "kid" here is [PasetoIssuerConfig.Footer], which the signature covers.
//
// The zero value is usable and safe: it stamps "iat", requires the caller to
// supply an expiry, and refuses to mint a token that would never expire.
type PasetoIssuerConfig struct {
	// Issuer, when non-empty, is stamped as "iss" on claims that carry none.
	Issuer string
	// Footer is the optional, AUTHENTICATED but unencrypted footer. It is
	// covered by the signature through the pre-authentication encoding, and it
	// is where a key identifier belongs in this format.
	Footer []byte
	// ImplicitAssertion is v4's optional out-of-band context: it is covered by
	// the signature but never transmitted, so a token only verifies for a
	// recipient who already knows it. Both sides must configure the same value.
	ImplicitAssertion []byte
	// Lifetime, when positive, is stamped as "exp" = now + Lifetime on claims
	// that carry no expiry of their own. Zero means the claims must bring one.
	Lifetime time.Duration
	// AllowMissingExpiry permits minting a token with no "exp" at all. The
	// zero value is false, so the dangerous shape is the one you have to ask
	// for by name (ADR 0030).
	AllowMissingExpiry bool
	// Clock is the time source for "iat" and the derived "exp". Nil means
	// clock.System.
	Clock clock.Clock
}

// issuing projects the PASETO config onto the shared issuing policy, so the
// claim-stamping rules are written once and cannot drift between the formats.
func (c PasetoIssuerConfig) issuing() IssuerConfig {
	//: Type and KeyID stay zero: PASETO carries no header to put them in.
	return IssuerConfig{
		Issuer:             c.Issuer,
		Lifetime:           c.Lifetime,
		AllowMissingExpiry: c.AllowMissingExpiry,
		Clock:              c.Clock,
	}
}
