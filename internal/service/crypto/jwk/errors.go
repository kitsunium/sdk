// Package jwk — declares the sentinel *errs.Error values for JWK / JWK Set
// parsing, serialisation and selection. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.
//
// None of these Public strings names a member VALUE: a JWK carries key
// material, and an error message is the one place it must never surface. The
// Private strings name the member and the rule that rejected it, never its
// contents.
package jwk

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR — a malformed JWK is a data problem,
// not a generic internal software error (70).
const exitDataErr int = 65

// httpBadRequest is the HTTP status a malformed JWK maps to: the document is
// client-bad input, not a server fault.
const httpBadRequest int = 400

// httpNotFound is the HTTP status an unresolvable "kid" maps to: the caller
// named a key the set does not hold.
const httpNotFound int = 404

var (
	// Malformed is returned when the document is not the JSON shape RFC 7517
	// describes — invalid JSON, a non-object JWK, or a non-array "keys".
	Malformed = errs.Define(CodeJWKMalformed, "MALFORMED",
		"JWK document is malformed",
		"service/crypto/jwk: json.Unmarshal rejected the document, or a JWK/JWK-Set member had the wrong JSON kind",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// MissingMember is returned when a member required for the declared key
	// type is absent or empty.
	MissingMember = errs.Define(CodeJWKMissingMember, "MISSING_MEMBER",
		"JWK is missing a required member",
		"service/crypto/jwk: a member required by the declared kty (kty/crv/x/y/k, or keys on a Set) is absent or empty",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// UnsupportedKeyType is returned for a "kty" this package does not model.
	// RSA is deliberately here: the SDK ships no RSA signer.
	UnsupportedKeyType = errs.Define(CodeJWKUnsupportedKeyType, "UNSUPPORTED_KEY_TYPE",
		"JWK key type is not supported",
		"service/crypto/jwk: kty is not EC, OKP or oct; RSA is unsupported because the SDK ships no RSA scheme",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// UnsupportedCurve is returned for an unknown "crv", or a known curve
	// paired with the wrong "kty".
	UnsupportedCurve = errs.Define(CodeJWKUnsupportedCurve, "UNSUPPORTED_CURVE",
		"JWK curve is not supported",
		"service/crypto/jwk: crv is unknown, or valid for a different kty (P-256 under OKP, Ed25519 under EC)",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// InvalidEncoding is returned when a member is not unpadded base64url, or
	// its decoded length is not the fixed size the declared curve mandates.
	InvalidEncoding = errs.Define(CodeJWKInvalidEncoding, "INVALID_ENCODING",
		"JWK member is not valid unpadded base64url",
		"service/crypto/jwk: base64.RawURLEncoding rejected a member, or its decoded length is not the curve's fixed octet length",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// KeyMismatch is returned when well-encoded material does not describe a
	// key on the declared curve — off-curve point, out-of-range scalar, or a
	// private member whose derived public key differs from the declared one.
	KeyMismatch = errs.Define(CodeJWKKeyMismatch, "KEY_MISMATCH",
		"JWK material does not match the declared curve",
		"service/crypto/jwk: point is off-curve or the identity, scalar is outside [1,n-1], or d does not derive the declared x/y",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// NoPublicForm is the public-serialisation refusal: a symmetric key has no
	// public half, so the public path cannot represent it without publishing
	// the secret. Only MarshalPrivate may emit it.
	NoPublicForm = errs.Define(CodeJWKNoPublicForm, "NO_PUBLIC_FORM",
		"Symmetric keys have no public JWK form",
		"service/crypto/jwk: MarshalPublic/MarshalJSON refused an oct key; the k member IS the secret, use MarshalPrivate deliberately")

	// NoPrivateMaterial is returned by a private-path call on a key that holds
	// only public members.
	NoPrivateMaterial = errs.Define(CodeJWKNoPrivateMaterial, "NO_PRIVATE_MATERIAL",
		"JWK holds no private key material",
		"service/crypto/jwk: MarshalPrivate/ECDSAPrivate/Ed25519Private/Secret called on a public-only key")

	// TypeMismatch is returned by a typed accessor called for the wrong "kty".
	TypeMismatch = errs.Define(CodeJWKTypeMismatch, "TYPE_MISMATCH",
		"JWK accessor does not match the key type",
		"service/crypto/jwk: a kty-specific accessor was called on a key of another type")

	// KeyNotFound is returned when a Set lookup resolves no key, including the
	// empty-kid lookup that would otherwise match every key carrying none.
	KeyNotFound = errs.Define(CodeJWKKeyNotFound, "KEY_NOT_FOUND",
		"No key in the set carries that key id",
		"service/crypto/jwk: Set.ByKid matched no member, or was called with an empty kid",
		errs.WithHTTPStatus(httpNotFound),
		errs.WithExitCode(exitDataErr))

	// AmbiguousKid is returned when a Set lookup matches more than one key.
	// Selecting one would depend on JSON member order; callers rotating keys
	// enumerate the candidates with AllByKid instead.
	AmbiguousKid = errs.Define(CodeJWKAmbiguousKid, "AMBIGUOUS_KID",
		"Several keys in the set carry that key id",
		"service/crypto/jwk: Set.ByKid matched more than one member; use AllByKid and choose explicitly (ADR 0031)",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))
)
