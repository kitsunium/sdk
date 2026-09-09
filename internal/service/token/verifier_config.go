// Package token — the JWT verification policy.
package token

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// VerifierConfig configures a JWT verifier.
//
// The zero value is usable and strict: it requires an expiry, allows no clock
// skew, checks no issuer or audience (there is none to check against), and
// applies the default bounds.
type VerifierConfig struct {
	// Issuer, when non-empty, must equal the token's "iss" (RFC 8725 §3.8).
	Issuer string
	// Audience, when non-empty, must appear in the token's "aud"
	// (RFC 8725 §3.9). A correctly signed token minted for another API is not
	// a valid token here.
	Audience string
	// RequireType, when non-empty, must equal the JWS "typ" header. It is the
	// mechanism RFC 8725 §3.12 asks for: two kinds of token from one issuer
	// need validation rules that reject each other's tokens.
	RequireType string
	// Leeway is the clock-skew allowance applied to "exp" and "nbf". It must
	// be in [0, MaxLeeway].
	Leeway time.Duration
	// MaxLifetime, when positive, refuses an authenticated token whose
	// exp-iat span exceeds it. It requires "iat": a lifetime that cannot be
	// computed is not a lifetime this recipient has accepted.
	MaxLifetime time.Duration
	// MaxTokenLen bounds the token string. Zero means DefaultMaxTokenLen; a
	// value outside [MinTokenLen, MaxTokenLenCeiling] is refused.
	MaxTokenLen int
	// MaxClaimDepth bounds the claims' JSON nesting. Zero means
	// DefaultMaxClaimDepth; a value outside [1, MaxClaimDepthCeiling] is
	// refused.
	MaxClaimDepth int
	// MaxKeyCandidates bounds how many keys a set verifier tries for one kid.
	// Zero means DefaultMaxKeyCandidates. Ignored by single-key verifiers.
	MaxKeyCandidates int
	// AllowMissingExpiry accepts a token with no "exp". The zero value is
	// false, so the profile that RFC 7519 §4.1.4 leaves optional is required
	// here unless the caller opts out by name.
	AllowMissingExpiry bool
	// Clock is the time source for the temporal claims. Nil means
	// clock.System; a clock.ManualClock makes an expiry test deterministic
	// without sleeping.
	Clock clock.Clock
}
