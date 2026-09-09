// Package token implements the two concrete security-token formats behind the
// core/token ports: JWT over JWS Compact Serialization (RFC 7519 + RFC 7515)
// and PASETO v4.public. It is stdlib-only and composes the SDK's own crypto
// schemes — HMAC-SHA-256 from service/crypto/hmacsha2, Ed25519 from
// service/crypto/ed25519sig — so it adds no dependency to internal/service.
//
// # Algorithm confusion is prevented by the constructor set, not by a check
//
// There is no NewVerifier(alg, key) here, and there is no verifier that reads
// the token's "alg" header to decide what to do. There is one constructor per
// algorithm, each accepting only the ONE Go type that algorithm can use:
//
//	NewHS256Verifier(secret crypto.Key,     cfg VerifierConfig)
//	NewES256Verifier(pub *ecdsa.PublicKey,  cfg VerifierConfig)
//	NewEdDSAVerifier(pub ed25519.PublicKey, cfg VerifierConfig)
//
// The textbook algorithm-confusion bug — an RSA/EC public key handed to
// HMAC-SHA-256 as a shared secret, so that anybody holding the public key can
// mint tokens — is a call that does not compile here: *ecdsa.PublicKey is not
// a crypto.Key and never converts to one. Verification then compares the
// header against the binding and refuses a mismatch with
// coretoken.AlgorithmMismatch, before any key reaches any primitive.
//
// [NewVerifierFromJWK] and [NewSetVerifier] are the only places an algorithm is
// chosen at run time, and they choose it from the KEY's "kty"/"crv" — material
// the relying party fetched from a trusted publisher — never from the token.
//
// # What is refused, always
//
//   - "alg":"none" in any capitalisation (RFC 7519 §6), with no option to
//     enable it: the core Algorithm enum has no value that spells it.
//   - a non-empty "crit" header (RFC 7515 §4.1.11).
//   - a token past the configured size, or claims past the configured nesting
//     depth, both checked before any decode.
//   - a repeated JSON member in the header or the claims (RFC 8725 §2.6).
//   - a token with no "exp", unless the profile opts out explicitly.
//
// See the package CLAUDE.md for the RFC 8725 coverage table, including the
// sections this package does NOT address.
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
