// Package token — the algorithm, registered-claim and JWK key vocabulary.
package token

import (
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

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

// KeyTypeEC is the JWK "kty" of an elliptic-curve key on a NIST prime curve
// (RFC 7518 §6.2): public members "x" and "y", private member "d".
const KeyTypeEC KeyType = jwk.TypeEC

// KeyTypeOKP is the JWK "kty" of an octet key pair, an Edwards-curve key
// (RFC 8037 §2): public member "x", private member "d".
const KeyTypeOKP KeyType = jwk.TypeOKP

// KeyTypeOct is the JWK "kty" of a symmetric key carried whole in "k"
// (RFC 7518 §6.4). It has no public half and no curve.
const KeyTypeOct KeyType = jwk.TypeOct

// CurveP256 is NIST P-256 (RFC 7518 §6.2.1.1), the curve of an ES256 key.
const CurveP256 Curve = jwk.CurveP256

// CurveP384 is NIST P-384. [ParseJWK] accepts it, but nothing in this package
// signs or verifies with it, so [NewVerifierFromJWK] refuses it with
// [KeyUnsuitable].
const CurveP384 Curve = jwk.CurveP384

// CurveP521 is NIST P-521, accepted and refused exactly as P-384 is.
const CurveP521 Curve = jwk.CurveP521

// CurveEd25519 is the Edwards curve of an EdDSA key (RFC 8037 §2).
const CurveEd25519 Curve = jwk.CurveEd25519
