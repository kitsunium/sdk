// Package token — the PASETO v4.public verification policy.
package token

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// PasetoVerifierConfig configures a PASETO v4.public verifier.
//
// Like its issuing counterpart it is flat rather than an embedded
// [VerifierConfig], and the absent knobs are deliberate: PASETO has no "typ"
// to require and no "kid" to select on, so RequireType and MaxKeyCandidates
// would be fields this format ignores.
//
// The zero value is usable and strict: it requires an expiry, allows no clock
// skew, checks no issuer or audience, expects no footer, and applies the
// default bounds.
type PasetoVerifierConfig struct {
	// Issuer, when non-empty, must equal the token's "iss" (RFC 8725 §3.8).
	Issuer string
	// Audience, when non-empty, must equal the token's "aud". PASETO's
	// audience is a single string, so there is no list to search.
	Audience string
	// Footer is the footer this verifier expects, compared in constant time.
	// Empty means the token must carry NO footer.
	//
	// The Verifier port returns claims, not a footer, so an unconstrained
	// footer would be data this package authenticated and then dropped on the
	// floor. Refusing is the only answer that does not quietly lose it.
	Footer []byte
	// ImplicitAssertion must equal the value the issuer signed with.
	ImplicitAssertion []byte
	// Leeway is the clock-skew allowance applied to "exp" and "nbf". It must
	// be in [0, MaxLeeway].
	Leeway time.Duration
	// MaxLifetime, when positive, refuses an authenticated token whose exp-iat
	// span exceeds it. It requires "iat".
	MaxLifetime time.Duration
	// MaxTokenLen bounds the token string. Zero means DefaultMaxTokenLen.
	MaxTokenLen int
	// MaxClaimDepth bounds the claims' JSON nesting. Zero means
	// DefaultMaxClaimDepth.
	MaxClaimDepth int
	// AllowMissingExpiry accepts a token with no "exp".
	AllowMissingExpiry bool
	// Clock is the time source for the temporal claims. Nil means
	// clock.System; a clock.ManualClock makes an expiry test deterministic.
	Clock clock.Clock
}

// verifying projects the PASETO config onto the shared verification policy, so
// the temporal and identity rules are written once for both formats.
func (c PasetoVerifierConfig) verifying() VerifierConfig {
	//: RequireType and MaxKeyCandidates stay zero: PASETO has no header to
	//: type and no key set to select from.
	return VerifierConfig{
		Issuer:             c.Issuer,
		Audience:           c.Audience,
		Leeway:             c.Leeway,
		MaxLifetime:        c.MaxLifetime,
		MaxTokenLen:        c.MaxTokenLen,
		MaxClaimDepth:      c.MaxClaimDepth,
		AllowMissingExpiry: c.AllowMissingExpiry,
		Clock:              c.Clock,
	}
}
