// Package token — declares the sentinel *errs.Error verdicts an issuer or a
// verifier returns. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// The Public strings are the half a third party reads (ADR 0005 §4). They name
// WHICH check refused the token and never echo a value from it: not the
// audience, not the issuer, not the key id, not a claim. A rejection message
// that quotes the token's own contents back is a mirror an attacker can query.
package token

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65): the input was not readable as
// a token at all, independently of any key or policy.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77): the token was read, and refused.
// The distinction from EX_DATAERR is the one an operator wants at 3am —
// "malformed" is a client bug, "refused" is an authentication decision.
const exitNoPerm int = 77

// exitConfig matches sysexits EX_CONFIG (78): the issuer or verifier itself is
// wrong, so every call fails until the construction site changes (ADR 0031).
const exitConfig int = 78

// statusUnauthorized is HTTP 401. Every refusal in this package maps to it:
// RFC 6750 §3.1 spends "invalid_token" on precisely this set, and a 400 would
// tell a client to change its request when the answer is to obtain a new token.
const statusUnauthorized int = 401

var (
	// Malformed is returned when the input cannot be read as a token: the
	// wrong segment count, a segment that is not unpadded base64url, a header
	// or payload that is not a JSON object.
	Malformed = errs.Define(CodeMalformed, "MALFORMED",
		"The token is malformed",
		"core/token: token structure unreadable — segment count, base64url decode, or JSON shape",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// AlgorithmNone is returned for a token presenting the unsecured "none"
	// algorithm (RFC 7519 §6), in any capitalisation. It is unconditional:
	// this domain has no Algorithm value for "none" and no option enabling it.
	AlgorithmNone = errs.Define(CodeAlgorithmNone, "ALGORITHM_NONE",
		"The token declares the unsecured none algorithm",
		"core/token: alg=none refused unconditionally; there is no configuration that accepts it",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// AlgorithmMismatch is returned when the token's algorithm header names
	// something other than the algorithm the verifier is bound to. It is the
	// verdict an algorithm-confusion attempt produces, and it is reached
	// BEFORE any key is handed to any primitive.
	AlgorithmMismatch = errs.Define(CodeAlgorithmMismatch, "ALGORITHM_MISMATCH",
		"The token algorithm is not the one this verifier accepts",
		"core/token: header alg differs from the bound algorithm; the header never selects a key",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// SignatureInvalid is returned when the signature or MAC tag did not
	// authenticate the token under the bound key. It is deliberately the same
	// verdict for "wrong key" and "tampered bytes": the two are the same event.
	SignatureInvalid = errs.Define(CodeSignatureInvalid, "SIGNATURE_INVALID",
		"The token signature is not valid",
		"core/token: signature or MAC tag did not verify under the bound key",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// Expired is returned when an AUTHENTICATED token's exp claim is in the
	// past. Reaching it means the signature already verified — claim verdicts
	// are never reported for a token that failed authentication.
	Expired = errs.Define(CodeExpired, "EXPIRED",
		"The token has expired",
		"core/token: exp is in the past after leeway; signature had already verified",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// NotYetValid is returned when an authenticated token's nbf claim is in
	// the future after leeway.
	NotYetValid = errs.Define(CodeNotYetValid, "NOT_YET_VALID",
		"The token is not valid yet",
		"core/token: nbf is in the future after leeway",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// ExpiryRequired is returned when a token carries no exp claim and the
	// profile requires one — which is the default, because RFC 7519 §4.1.4
	// makes exp OPTIONAL and a token that never expires is a password.
	ExpiryRequired = errs.Define(CodeExpiryRequired, "EXPIRY_REQUIRED",
		"The token carries no expiry",
		"core/token: exp absent and the profile requires it; set AllowMissingExpiry to opt out",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// AudienceMismatch is returned when an authenticated token's aud claim
	// does not contain this recipient. A correctly signed token addressed to
	// another API is not a valid token here (RFC 8725 §3.9).
	AudienceMismatch = errs.Define(CodeAudienceMismatch, "AUDIENCE_MISMATCH",
		"The token is not addressed to this audience",
		"core/token: configured audience absent from the aud claim",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// IssuerMismatch is returned when an authenticated token's iss claim is
	// not the configured issuer (RFC 8725 §3.8).
	IssuerMismatch = errs.Define(CodeIssuerMismatch, "ISSUER_MISMATCH",
		"The token issuer is not the expected one",
		"core/token: iss differs from the configured issuer",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// TooLarge is returned when an input passes a declared size bound before
	// any parsing happens — the token string, the decoded header, the claim
	// count. The bound is checked FIRST so a hostile input never funds the
	// work of discovering that it is hostile.
	TooLarge = errs.Define(CodeTooLarge, "TOO_LARGE",
		"The token exceeds the accepted size",
		"core/token: input past a declared bound (token length, header size, or claim count)",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// TooDeep is returned when the claims payload nests deeper than the
	// configured bound. The depth is measured by a linear scan of the raw
	// bytes, before encoding/json ever sees them.
	TooDeep = errs.Define(CodeTooDeep, "TOO_DEEP",
		"The token claims are nested too deeply",
		"core/token: claim nesting past MaxClaimDepth, measured before JSON decoding",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// KeyUnsuitable is returned when a key cannot serve the algorithm it was
	// handed to: wrong length, wrong curve, or a key type for which this
	// domain implements nothing.
	KeyUnsuitable = errs.Define(CodeKeyUnsuitable, "KEY_UNSUITABLE",
		"The key cannot be used for this token algorithm",
		"core/token: key type, curve or length does not match the bound algorithm",
		errs.WithExitCode(exitConfig))

	// PolicyMisconfigured is returned by every call to an issuer or verifier
	// built with a configuration it cannot honour. Like its resilience
	// namesake it is permanent, not transient: the fix is at the construction
	// site, never a retry (ADR 0031).
	PolicyMisconfigured = errs.Define(CodePolicyMisconfigured, "POLICY_MISCONFIGURED",
		"The token policy is misconfigured",
		"core/token: issuer or verifier built with a configuration it cannot honour; fields name the knob",
		errs.WithExitCode(exitConfig))

	// IssueFailed is returned when an issuer cannot render the claims it was
	// given — a claim the target format cannot express, or a signing fault.
	IssueFailed = errs.Define(CodeIssueFailed, "ISSUE_FAILED",
		"The token could not be issued",
		"core/token: claims unrenderable in the target format, or the signing primitive failed",
		errs.WithExitCode(exitConfig))

	// ClaimNameInvalid is returned by ClaimsValue.WithPrivateRaw for an empty
	// name or one shadowing a registered claim.
	ClaimNameInvalid = errs.Define(CodeClaimNameInvalid, "CLAIM_NAME_INVALID",
		"The claim name is empty or reserved",
		"core/token: private claim name empty or colliding with a registered claim name",
		errs.WithExitCode(exitDataErr))

	// LifetimeTooLong is returned when an authenticated token's exp-iat span
	// exceeds the maximum the verifier accepts. A long-lived bearer token is
	// a credential with no revocation story, so a recipient is allowed to say
	// no to one even when its issuer said yes.
	LifetimeTooLong = errs.Define(CodeLifetimeTooLong, "LIFETIME_TOO_LONG",
		"The token lifetime exceeds what this recipient accepts",
		"core/token: exp-iat span past MaxLifetime",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))
)
