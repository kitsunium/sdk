// Package token — the dotted-quad codes a consumer routes on.
//
// These are RE-EXPORTS, not declarations: the ranges 0.2.13.*, 0.3.44.* and
// 0.3.42.* are owned by internal/core/token, internal/service/token and
// internal/service/crypto/jwk, and the errs ownership audit skips a
// cross-package selector for exactly this reason (ADR 0035). Matching on a code
// rather than on a reason string is the stronger contract — a code is a number
// in docs/error-codes.yaml, a reason is a spelling.
//
//	switch {
//	case errs.HasCode(err, token.CodeExpired):          // 401, refresh
//	case errs.HasCode(err, token.CodeAudienceMismatch): // 401, wrong service
//	case errs.HasCode(err, token.CodeSignatureInvalid): // 401, and alert
//	}
//
// All three answer 401, and a token addressed to another service is no
// exception: RFC 6750 §3.1 spends invalid_token — and 401 — on a token that is
// "invalid for other reasons", while 403 (insufficient_scope) says the token
// is valid HERE and merely too weak, which an audience mismatch is not. What
// differs between the rows is what the caller does next, not the status.
package token

import (
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// CodeMalformed identifies an input that could not be read as a token
// (0.2.13.1).
const CodeMalformed errs.Code = coretoken.CodeMalformed

// CodeAlgorithmNone identifies a token declaring the unsecured "none"
// algorithm (0.2.13.2).
const CodeAlgorithmNone errs.Code = coretoken.CodeAlgorithmNone

// CodeAlgorithmMismatch identifies a header naming an algorithm other than the
// bound one — what an algorithm-confusion attempt produces (0.2.13.3).
const CodeAlgorithmMismatch errs.Code = coretoken.CodeAlgorithmMismatch

// CodeSignatureInvalid identifies a signature or MAC tag that did not
// authenticate (0.2.13.4).
const CodeSignatureInvalid errs.Code = coretoken.CodeSignatureInvalid

// CodeExpired identifies an authenticated token whose exp is past (0.2.13.5).
const CodeExpired errs.Code = coretoken.CodeExpired

// CodeNotYetValid identifies an authenticated token whose nbf is in the future
// (0.2.13.6).
const CodeNotYetValid errs.Code = coretoken.CodeNotYetValid

// CodeExpiryRequired identifies a token with no exp where the profile requires
// one (0.2.13.7).
const CodeExpiryRequired errs.Code = coretoken.CodeExpiryRequired

// CodeAudienceMismatch identifies a configured audience absent from the token's
// aud claim (0.2.13.8).
const CodeAudienceMismatch errs.Code = coretoken.CodeAudienceMismatch

// CodeIssuerMismatch identifies an iss that is not the configured issuer
// (0.2.13.9).
const CodeIssuerMismatch errs.Code = coretoken.CodeIssuerMismatch

// CodeTooLarge identifies an input past a declared size bound (0.2.13.10).
const CodeTooLarge errs.Code = coretoken.CodeTooLarge

// CodeTooDeep identifies claims nested past MaxClaimDepth (0.2.13.11).
const CodeTooDeep errs.Code = coretoken.CodeTooDeep

// CodeKeyUnsuitable identifies a key that cannot serve the algorithm it was
// handed to (0.2.13.12).
const CodeKeyUnsuitable errs.Code = coretoken.CodeKeyUnsuitable

// CodePolicyMisconfigured identifies a constructor given a configuration it
// cannot honour; permanent, never a retry (0.2.13.13).
const CodePolicyMisconfigured errs.Code = coretoken.CodePolicyMisconfigured

// CodeIssueFailed identifies claims that could not be rendered or signed
// (0.2.13.14).
const CodeIssueFailed errs.Code = coretoken.CodeIssueFailed

// CodeClaimNameInvalid identifies an empty or reserved private-claim name
// (0.2.13.15).
const CodeClaimNameInvalid errs.Code = coretoken.CodeClaimNameInvalid

// CodeLifetimeTooLong identifies an exp-iat span past
// VerifierConfig.MaxLifetime (0.2.13.16).
const CodeLifetimeTooLong errs.Code = coretoken.CodeLifetimeTooLong

// CodeHeaderUnsupported identifies a non-empty "crit", or a "typ" mismatch
// (0.3.44.1).
const CodeHeaderUnsupported errs.Code = svctoken.CodeHeaderUnsupported

// CodeKeyNotFound identifies a kid naming no key in the set (0.3.44.2).
const CodeKeyNotFound errs.Code = svctoken.CodeKeyNotFound

// CodeKeyIDMissing identifies a token presented to a set verifier with no kid
// (0.3.44.3).
const CodeKeyIDMissing errs.Code = svctoken.CodeKeyIDMissing

// CodeKeyIDAmbiguous identifies more keys sharing a kid than the verifier will
// try (0.3.44.4).
const CodeKeyIDAmbiguous errs.Code = svctoken.CodeKeyIDAmbiguous

// CodeFooterMismatch identifies a PASETO footer that is not the expected one
// (0.3.44.5).
const CodeFooterMismatch errs.Code = svctoken.CodeFooterMismatch

// CodeSchemeUnsupported identifies a PASETO version+purpose that is not
// v4.public (0.3.44.6).
const CodeSchemeUnsupported errs.Code = svctoken.CodeSchemeUnsupported

// CodeDuplicateMember identifies a header or claims object repeating a member
// name (0.3.44.7).
const CodeDuplicateMember errs.Code = svctoken.CodeDuplicateMember

// CodeJWKMalformed identifies a key document that is not the JSON shape
// RFC 7517 describes — invalid JSON, a key that is not an object, or a set
// whose "keys" is not an array (0.3.42.1).
const CodeJWKMalformed errs.Code = jwk.CodeJWKMalformed

// CodeJWKMissingMember identifies a key missing a member its declared type
// requires, or a set missing "keys" (0.3.42.2).
const CodeJWKMissingMember errs.Code = jwk.CodeJWKMissingMember

// CodeJWKUnsupportedKeyType identifies a "kty" other than EC, OKP or oct — RSA
// included, since the SDK verifies nothing with RSA (0.3.42.3).
const CodeJWKUnsupportedKeyType errs.Code = jwk.CodeJWKUnsupportedKeyType

// CodeJWKUnsupportedCurve identifies an unknown "crv", or one paired with the
// wrong "kty" (0.3.42.4).
const CodeJWKUnsupportedCurve errs.Code = jwk.CodeJWKUnsupportedCurve

// CodeJWKInvalidEncoding identifies a member that is not unpadded base64url, or
// not the fixed length its curve mandates (0.3.42.5).
const CodeJWKInvalidEncoding errs.Code = jwk.CodeJWKInvalidEncoding

// CodeJWKKeyMismatch identifies material that is not a key on the declared
// curve: an off-curve point, an out-of-range scalar, or a "d" that does not
// derive the declared public key (0.3.42.6).
const CodeJWKKeyMismatch errs.Code = jwk.CodeJWKKeyMismatch
