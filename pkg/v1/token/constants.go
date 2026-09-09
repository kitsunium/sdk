// Package token — the algorithm and registered-claim vocabulary.
package token

import coretoken "github.com/kitsunium/sdk/internal/core/token"

// AlgorithmHS256 is JOSE "HS256" — HMAC-SHA-256 over a 256-bit shared secret
// (RFC 7518 §3.2). Symmetric: a verifier can also mint.
const AlgorithmHS256 Algorithm = coretoken.AlgorithmHS256

// AlgorithmES256 is JOSE "ES256" — ECDSA on NIST P-256 with SHA-256 and the
// fixed-width R||S signature encoding of RFC 7518 §3.4.
const AlgorithmES256 Algorithm = coretoken.AlgorithmES256

// AlgorithmEdDSA is JOSE "EdDSA" — Ed25519 over the JWS signing input
// (RFC 8037 §3.1).
const AlgorithmEdDSA Algorithm = coretoken.AlgorithmEdDSA

// AlgorithmPasetoV4Public is PASETO v4.public — Ed25519 over the
// pre-authentication encoding of the header, payload and footer.
const AlgorithmPasetoV4Public Algorithm = coretoken.AlgorithmPasetoV4Public

// There is deliberately no constant for the unsecured "none" algorithm of
// RFC 7519 §6: [Algorithm] is an enum with no value that spells it, so no
// configuration can enable one and no call site can ask for one.

// ClaimIssuer is the registered claim "iss" (RFC 7519 §4.1.1).
const ClaimIssuer string = coretoken.ClaimIssuer

// ClaimSubject is the registered claim "sub" (RFC 7519 §4.1.2).
const ClaimSubject string = coretoken.ClaimSubject

// ClaimAudience is the registered claim "aud" (RFC 7519 §4.1.3).
const ClaimAudience string = coretoken.ClaimAudience

// ClaimExpiry is the registered claim "exp" (RFC 7519 §4.1.4).
const ClaimExpiry string = coretoken.ClaimExpiry

// ClaimNotBefore is the registered claim "nbf" (RFC 7519 §4.1.5).
const ClaimNotBefore string = coretoken.ClaimNotBefore

// ClaimIssuedAt is the registered claim "iat" (RFC 7519 §4.1.6).
const ClaimIssuedAt string = coretoken.ClaimIssuedAt

// ClaimID is the registered claim "jti" (RFC 7519 §4.1.7).
const ClaimID string = coretoken.ClaimID
