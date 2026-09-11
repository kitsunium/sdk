// Package token — the constructor set, one per algorithm, plus the JWK
// bridges. Each constructor accepts only the Go key type its algorithm can
// use, which is what makes algorithm confusion a call that does not compile.
package token

import (
	"crypto/ecdsa"
	"crypto/ed25519"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// Key is the public alias for the SDK's opaque, redacting 256-bit symmetric
// key — the only type NewHS256Issuer / NewHS256Verifier accept. Build one with
// pkg/v1/crypto.NewKey, or derive one with pkg/v1/kdf; never type one.
type Key = corecrypto.Key

// JWK is the public alias for a parsed JSON Web Key.
type JWK = jwk.KeyValue

// JWKSet is the public alias for a parsed JSON Web Key Set.
type JWKSet = jwk.Set

// NewHS256Issuer returns a JWT issuer signing with HMAC-SHA-256 under secret.
//
// HS256 is symmetric: everyone who can verify these tokens can also mint them.
// Reach for [NewES256Issuer] or [NewEdDSAIssuer] the moment the verifier is not
// in the same trust domain as the issuer.
func NewHS256Issuer(secret Key, cfg IssuerConfig) (issuer Issuer, err error) {
	//: delegate to the service constructor.
	return svctoken.NewHS256Issuer(secret, cfg)
}

// NewES256Issuer returns a JWT issuer signing with ECDSA P-256 + SHA-256,
// emitting the fixed-width R||S signature of RFC 7518 §3.4.
func NewES256Issuer(priv *ecdsa.PrivateKey, cfg IssuerConfig) (issuer Issuer, err error) {
	//: delegate to the service constructor.
	return svctoken.NewES256Issuer(priv, cfg)
}

// NewEdDSAIssuer returns a JWT issuer signing with Ed25519 (JOSE "EdDSA").
func NewEdDSAIssuer(priv ed25519.PrivateKey, cfg IssuerConfig) (issuer Issuer, err error) {
	//: delegate to the service constructor.
	return svctoken.NewEdDSAIssuer(priv, cfg)
}

// NewHS256Verifier returns a JWT verifier bound to HMAC-SHA-256 under secret.
// It accepts a [Key] and nothing else, which is what makes the HMAC half of
// algorithm confusion unwritable rather than merely documented.
func NewHS256Verifier(secret Key, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewHS256Verifier(secret, cfg)
}

// NewES256Verifier returns a JWT verifier bound to ECDSA P-256 under pub. The
// point is validated on the curve, not merely measured (RFC 8725 §3.4).
func NewES256Verifier(pub *ecdsa.PublicKey, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewES256Verifier(pub, cfg)
}

// NewEdDSAVerifier returns a JWT verifier bound to Ed25519 under pub.
func NewEdDSAVerifier(pub ed25519.PublicKey, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewEdDSAVerifier(pub, cfg)
}

// NewPasetoV4Issuer returns a PASETO v4.public issuer signing with Ed25519.
func NewPasetoV4Issuer(priv ed25519.PrivateKey, cfg PasetoIssuerConfig) (issuer Issuer, err error) {
	//: delegate to the service constructor.
	return svctoken.NewPasetoV4Issuer(priv, cfg)
}

// NewPasetoV4Verifier returns a PASETO v4.public verifier bound to pub. A token
// of any other PASETO version or purpose — including v4.local — is refused with
// [SchemeUnsupported].
func NewPasetoV4Verifier(pub ed25519.PublicKey, cfg PasetoVerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewPasetoV4Verifier(pub, cfg)
}

// NewVerifierFromJWK returns a JWT verifier bound to the algorithm key's own
// kty/crv imply: oct to HS256, EC P-256 to ES256, OKP Ed25519 to EdDSA. Every
// other key — RSA, P-384, P-521 — is refused with [KeyUnsuitable].
//
// The key's advisory members are enforced: a "use" other than "sig", a
// "key_ops" without "verify", or an "alg" contradicting the key type all refuse
// the key.
func NewVerifierFromJWK(key JWK, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewVerifierFromJWK(key, cfg)
}

// NewSetVerifier returns a JWT verifier selecting a key from set by the token's
// "kid" header.
//
// The kid chooses which key to TRY; the signature decides whether the token is
// valid. A token with no kid is refused with [KeyIDMissing] rather than tried
// against every key in the set. When several keys share a kid — legal during a
// rotation, since RFC 7517 §4.5 only SHOULD-s uniqueness — the candidates are
// tried in document order up to VerifierConfig.MaxKeyCandidates, past which the
// token is refused with [KeyIDAmbiguous].
func NewSetVerifier(set JWKSet, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewSetVerifier(set, cfg)
}
