// Package jwk — range 0.3.42.* (ADR 0005 service/crypto/jwk block).
package jwk

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.42.0 - 0.3.42.255

// CodeJWKMalformed identifies a document that is not the JSON shape RFC 7517
// describes — invalid JSON, a non-object where a JWK was expected, or a JWK Set
// whose "keys" member is not an array.
const CodeJWKMalformed errs.Code = 0x00_03_2A_01 // 0.3.42.1

// CodeJWKMissingMember identifies a JWK (or JWK Set) whose required member for
// the declared key type is absent or empty — "kty", "crv", "x", "k", "keys".
const CodeJWKMissingMember errs.Code = 0x00_03_2A_02 // 0.3.42.2

// CodeJWKUnsupportedKeyType identifies a "kty" this package does not model.
// RSA lands here on purpose: the SDK ships no RSA signer, so representing an
// RSA key would promise an interop the crypto domain cannot honour.
const CodeJWKUnsupportedKeyType errs.Code = 0x00_03_2A_03 // 0.3.42.3

// CodeJWKUnsupportedCurve identifies a "crv" that is unknown, or known but
// paired with the wrong "kty" (P-256 under OKP, Ed25519 under EC).
const CodeJWKUnsupportedCurve errs.Code = 0x00_03_2A_04 // 0.3.42.4

// CodeJWKInvalidEncoding identifies a member that is not unpadded base64url
// (RFC 7515 §2), or whose decoded length is not the fixed size the declared
// curve mandates (RFC 7518 §6.2.1.2 forbids stripping leading zero octets).
const CodeJWKInvalidEncoding errs.Code = 0x00_03_2A_05 // 0.3.42.5

// CodeJWKKeyMismatch identifies well-encoded material that does not describe a
// key on the declared curve: an off-curve or identity point, a private scalar
// outside [1, n-1], or a "d" whose derived public key differs from the declared
// "x"/"y". It is the one check a naive JWK parser skips.
const CodeJWKKeyMismatch errs.Code = 0x00_03_2A_06 // 0.3.42.6

// CodeJWKNoPublicForm identifies the public-serialisation refusal: a symmetric
// ("oct") key has no public half, so emitting it on the public path would
// publish the secret itself. See ADR 0030 — the default is never the dangerous
// one.
const CodeJWKNoPublicForm errs.Code = 0x00_03_2A_07 // 0.3.42.7

// CodeJWKNoPrivateMaterial identifies a private-path call on a key that holds
// only public members — MarshalPrivate, Ed25519Private, ECDSAPrivate, Secret.
const CodeJWKNoPrivateMaterial errs.Code = 0x00_03_2A_08 // 0.3.42.8

// CodeJWKTypeMismatch identifies a typed accessor called for the wrong "kty" —
// Ed25519Public on an EC key, Secret on an OKP key.
const CodeJWKTypeMismatch errs.Code = 0x00_03_2A_09 // 0.3.42.9

// CodeJWKKeyNotFound identifies a Set lookup whose "kid" matches no member, or
// a lookup with an empty "kid" (which would otherwise match every key that
// carries none).
const CodeJWKKeyNotFound errs.Code = 0x00_03_2A_0A // 0.3.42.10

// CodeJWKAmbiguousKid identifies a Set lookup whose "kid" matches more than one
// member. Picking one would depend on JSON member order; rotation callers use
// AllByKid and decide explicitly (ADR 0031 — refuse rather than guess).
const CodeJWKAmbiguousKid errs.Code = 0x00_03_2A_0B // 0.3.42.11
