// Package token — the constructor set, one per algorithm, plus the JWK
// loaders and bridges. Each constructor accepts only the Go key type its
// algorithm can use, which is what makes algorithm confusion a call that does
// not compile.
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

// JWK is the public alias for a parsed JSON Web Key (RFC 7517). Obtain one
// from [ParseJWK]: the type has no exported field and deliberately no
// UnmarshalJSON, so json.Unmarshal decodes nothing into a JWK — and reports no
// error. A JWK field in a configuration struct therefore stays the zero JWK,
// which [NewVerifierFromJWK] refuses with [KeyUnsuitable]; decode that field
// as a json.RawMessage and hand it to ParseJWK instead.
//
// json.Marshal renders a JWK's public members only. A symmetric ("oct") key
// has no public form — its "k" member is the secret itself — so json.Marshal
// refuses it rather than publish it; private members leave only through the
// key's MarshalPrivate method, which says so at the call site.
type JWK = jwk.KeyValue

// JWKSet is the public alias for a parsed JSON Web Key Set (RFC 7517 §5).
// Obtain one from [ParseJWKSet], or assemble one from parsed keys with
// [NewJWKSet].
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

// ParseJWK decodes one JSON Web Key document (RFC 7517 §4) into the [JWK] that
// [NewVerifierFromJWK] and [NewJWKSet] take. Fetching the document is the
// application's job: this package performs no I/O, so the bytes come from
// wherever the application already trusts — a configuration file, or a key
// endpoint it queried itself.
//
// It is the only way to turn bytes into a JWK, and it validates before it
// returns one: every member the key type requires present, every member
// unpadded base64url (RFC 7515 §2), coordinates at the curve's fixed length,
// the point on the declared curve, and — when a private "d" is present — the
// scalar deriving exactly the declared public key. Members it does not model
// are ignored (RFC 7517 §4).
//
// A refusal returns the zero JWK and an error carrying one of
// [CodeJWKMalformed], [CodeJWKMissingMember], [CodeJWKUnsupportedKeyType],
// [CodeJWKUnsupportedCurve], [CodeJWKInvalidEncoding] or [CodeJWKKeyMismatch];
// its message names the rule that failed and never a member's value. An RSA
// key is refused here, with [CodeJWKUnsupportedKeyType], because the SDK
// verifies nothing with RSA.
func ParseJWK(document []byte) (key JWK, err error) {
	//: delegate to the single validating entry point.
	return jwk.Parse(document)
}

// ParseJWKSet decodes a JSON Web Key Set document (RFC 7517 §5) — typically the
// body an issuer serves at its jwks_uri, fetched by the application — into the
// [JWKSet] that [NewSetVerifier] takes.
//
// Every member is validated exactly as [ParseJWK] validates one, and the set is
// accepted whole or not at all: one refused member refuses the document, with
// that member's own CodeJWK* code and its index attached, rather than yielding
// a set that silently holds fewer keys than were published. A set carrying an
// RSA member is therefore refused, with [CodeJWKUnsupportedKeyType].
//
// The "keys" member is required — absent or null is refused with
// [CodeJWKMissingMember] — while an empty array is a valid empty set, which
// [NewSetVerifier] then refuses because it could never verify anything. A
// document, or a member, that is JSON null is not an object at all and is
// refused with [CodeJWKMalformed].
func ParseJWKSet(document []byte) (set JWKSet, err error) {
	//: delegate to the set decoder, which runs every member through the same
	//: validating entry point as ParseJWK.
	return jwk.ParseSet(document)
}

// NewJWKSet assembles a [JWKSet] from keys already parsed, in the given order —
// the order in which [NewSetVerifier] tries candidates sharing a "kid". It
// validates nothing further: the material of every non-zero JWK already passed
// [ParseJWK], and a zero JWK in a set can never verify a token.
func NewJWKSet(keys ...JWK) JWKSet {
	//: delegate; the set copies the slice, so a later edit of the caller's
	//: slice cannot reach it.
	return jwk.NewSet(keys...)
}

// NewVerifierFromJWK returns a JWT verifier bound to the algorithm key's own
// kty/crv imply: oct to HS256, EC P-256 to ES256, OKP Ed25519 to EdDSA. Every
// other key the SDK can represent — P-384, P-521 — is refused with
// [KeyUnsuitable]; an RSA key never gets this far, because [ParseJWK] refuses
// it with [CodeJWKUnsupportedKeyType].
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
//
// A set no token could ever verify against is refused here, with
// [PolicyMisconfigured], rather than built into a verifier that refuses
// everything: an empty set, and one whose every member either carries no kid
// or is a key this package does not verify with (P-384, P-521). A single key
// published without a kid belongs to [NewVerifierFromJWK].
func NewSetVerifier(set JWKSet, cfg VerifierConfig) (verifier Verifier, err error) {
	//: delegate to the service constructor.
	return svctoken.NewSetVerifier(set, cfg)
}
