package token

import "time"

const (
	// DefaultMaxTokenLen is the token-string bound applied when a config
	// leaves MaxTokenLen at zero. 8 KiB comfortably holds an OIDC ID token
	// with a certificate thumbprint; it does not hold an attack.
	DefaultMaxTokenLen int = 8 << 10
	// MinTokenLen is the floor a configured MaxTokenLen may not go below. A
	// bound smaller than this would reject every real token, which is a
	// configuration mistake worth naming rather than a policy.
	MinTokenLen int = 64
	// MaxTokenLenCeiling is the ceiling a configured MaxTokenLen may not
	// exceed. There is no legitimate multi-megabyte bearer token, and the
	// ceiling is what keeps "configurable" from meaning "unbounded".
	MaxTokenLenCeiling int = 1 << 20
	// DefaultMaxClaimDepth is the JSON nesting bound applied when a config
	// leaves MaxClaimDepth at zero.
	DefaultMaxClaimDepth int = 16
	// MaxClaimDepthCeiling is the ceiling a configured MaxClaimDepth may not
	// exceed.
	MaxClaimDepthCeiling int = 64
	// DefaultMaxKeyCandidates is how many keys a set verifier will try for one
	// "kid" when the config leaves MaxKeyCandidates at zero. A rotation window
	// holds two; four leaves room for a slow rollover without turning a
	// published JWK Set into a signature-verification multiplier.
	DefaultMaxKeyCandidates int = 4
	// MaxKeyCandidatesCeiling caps MaxKeyCandidates. Trying keys IS signature
	// verification, the expensive half of this package; a publisher who can
	// name the candidate count can name the cost.
	MaxKeyCandidatesCeiling int = 16
	// maxHeaderLen bounds the DECODED JOSE header. A header carries an alg, a
	// typ and a kid; anything larger is not a header this package will read.
	maxHeaderLen int = 4 << 10
	// decimalBase is the radix every integer this package renders uses.
	decimalBase int = 10
)

// MaxLeeway is the largest clock-skew allowance a verifier accepts.
//
// Leeway exists because two hosts disagree about the time by seconds. A leeway
// measured in hours is not skew tolerance, it is an expiry claim that has been
// quietly disabled — so the constructor refuses it rather than honouring a
// value whose effect the caller almost certainly did not intend (ADR 0031).
const MaxLeeway time.Duration = 5 * time.Minute
