// Package token — declares the sentinel *errs.Error verdicts an issuer or a
// verifier returns, and the refusals specific to the two concrete formats the
// engine in internal/service/security/token implements (ADR 0160). Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
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
		"core/security/token: token structure unreadable — segment count, base64url decode, or JSON shape",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// AlgorithmNone is returned for a token presenting the unsecured "none"
	// algorithm (RFC 7519 §6), in any capitalisation. It is unconditional:
	// this domain has no Algorithm value for "none" and no option enabling it.
	AlgorithmNone = errs.Define(CodeAlgorithmNone, "ALGORITHM_NONE",
		"The token declares the unsecured none algorithm",
		"core/security/token: alg=none refused unconditionally; there is no configuration that accepts it",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// AlgorithmMismatch is returned when the token's algorithm header names
	// something other than the algorithm the verifier is bound to. It is the
	// verdict an algorithm-confusion attempt produces, and it is reached
	// BEFORE any key is handed to any primitive.
	AlgorithmMismatch = errs.Define(CodeAlgorithmMismatch, "ALGORITHM_MISMATCH",
		"The token algorithm is not the one this verifier accepts",
		"core/security/token: header alg differs from the bound algorithm; the header never selects a key",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// SignatureInvalid is returned when the signature or MAC tag did not
	// authenticate the token under the bound key. It is deliberately the same
	// verdict for "wrong key" and "tampered bytes": the two are the same event.
	SignatureInvalid = errs.Define(CodeSignatureInvalid, "SIGNATURE_INVALID",
		"The token signature is not valid",
		"core/security/token: signature or MAC tag did not verify under the bound key",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// Expired is returned when an AUTHENTICATED token's exp claim is in the
	// past. Reaching it means the signature already verified — claim verdicts
	// are never reported for a token that failed authentication.
	Expired = errs.Define(CodeExpired, "EXPIRED",
		"The token has expired",
		"core/security/token: exp is in the past after leeway; signature had already verified",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// NotYetValid is returned when an authenticated token's nbf claim is in
	// the future after leeway.
	NotYetValid = errs.Define(CodeNotYetValid, "NOT_YET_VALID",
		"The token is not valid yet",
		"core/security/token: nbf is in the future after leeway",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// ExpiryRequired is returned when a token carries no exp claim and the
	// profile requires one — which is the default, because RFC 7519 §4.1.4
	// makes exp OPTIONAL and a token that never expires is a password.
	ExpiryRequired = errs.Define(CodeExpiryRequired, "EXPIRY_REQUIRED",
		"The token carries no expiry",
		"core/security/token: exp absent and the profile requires it; set AllowMissingExpiry to opt out",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// AudienceMismatch is returned when an authenticated token's aud claim
	// does not contain this recipient. A correctly signed token addressed to
	// another API is not a valid token here (RFC 8725 §3.9).
	AudienceMismatch = errs.Define(CodeAudienceMismatch, "AUDIENCE_MISMATCH",
		"The token is not addressed to this audience",
		"core/security/token: configured audience absent from the aud claim",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// IssuerMismatch is returned when an authenticated token's iss claim is
	// not the configured issuer (RFC 8725 §3.8).
	IssuerMismatch = errs.Define(CodeIssuerMismatch, "ISSUER_MISMATCH",
		"The token issuer is not the expected one",
		"core/security/token: iss differs from the configured issuer",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// TooLarge is returned when an input passes a declared size bound before
	// any parsing happens — the token string, the decoded header, the claim
	// count. The bound is checked FIRST so a hostile input never funds the
	// work of discovering that it is hostile.
	TooLarge = errs.Define(CodeTooLarge, "TOO_LARGE",
		"The token exceeds the accepted size",
		"core/security/token: input past a declared bound (token length, header size, or claim count)",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// TooDeep is returned when the claims payload nests deeper than the
	// configured bound. The depth is measured by a linear scan of the raw
	// bytes, before encoding/json ever sees them.
	TooDeep = errs.Define(CodeTooDeep, "TOO_DEEP",
		"The token claims are nested too deeply",
		"core/security/token: claim nesting past MaxClaimDepth, measured before JSON decoding",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))

	// KeyUnsuitable is returned when a key cannot serve the algorithm it was
	// handed to: wrong length, wrong curve, or a key type for which this
	// domain implements nothing.
	KeyUnsuitable = errs.Define(CodeKeyUnsuitable, "KEY_UNSUITABLE",
		"The key cannot be used for this token algorithm",
		"core/security/token: key type, curve or length does not match the bound algorithm",
		errs.WithExitCode(exitConfig))

	// PolicyMisconfigured is returned by every call to an issuer or verifier
	// built with a configuration it cannot honour. Like its resilience
	// namesake it is permanent, not transient: the fix is at the construction
	// site, never a retry (ADR 0031).
	PolicyMisconfigured = errs.Define(CodePolicyMisconfigured, "POLICY_MISCONFIGURED",
		"The token policy is misconfigured",
		"core/security/token: issuer or verifier built with a configuration it cannot honour; fields name the knob",
		errs.WithExitCode(exitConfig))

	// IssueFailed is returned when an issuer cannot render the claims it was
	// given — a claim the target format cannot express, or a signing fault.
	IssueFailed = errs.Define(CodeIssueFailed, "ISSUE_FAILED",
		"The token could not be issued",
		"core/security/token: claims unrenderable in the target format, or the signing primitive failed",
		errs.WithExitCode(exitConfig))

	// ClaimNameInvalid is returned by ClaimsValue.WithPrivateRaw for an empty
	// name or one shadowing a registered claim.
	ClaimNameInvalid = errs.Define(CodeClaimNameInvalid, "CLAIM_NAME_INVALID",
		"The claim name is empty or reserved",
		"core/security/token: private claim name empty or colliding with a registered claim name",
		errs.WithExitCode(exitDataErr))

	// LifetimeTooLong is returned when an authenticated token's exp-iat span
	// exceeds the maximum the verifier accepts. A long-lived bearer token is
	// a credential with no revocation story, so a recipient is allowed to say
	// no to one even when its issuer said yes.
	LifetimeTooLong = errs.Define(CodeLifetimeTooLong, "LIFETIME_TOO_LONG",
		"The token lifetime exceeds what this recipient accepts",
		"core/security/token: exp-iat span past MaxLifetime",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// The refusals specific to the two concrete formats (0.3.44.*). They are
	// raised by internal/service/security/token — their Private names that
	// package, the one that raises them — and declared here since ADR 0160, so
	// every code of the domain is in one place. As above, the Public half names
	// the check that refused the token and never echoes a value out of it — not
	// a kid, not a footer, not a claim.

	// HeaderUnsupported is returned for a JOSE header the engine will not act
	// on: a non-empty "crit", or a "typ" other than the required one.
	//
	// A non-empty "crit" is a rejection and not a warning because RFC 7515
	// §4.1.11 says so: the parameters it names MUST be understood, the engine
	// understands none of them, and "ignore what you do not understand" is how
	// a security-relevant extension gets silently dropped.
	HeaderUnsupported = errs.Define(CodeHeaderUnsupported, "HEADER_UNSUPPORTED",
		"The token header carries parameters this verifier will not accept",
		"service/security/token: non-empty crit, or typ differs from the required value",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyNotFound is returned when the token's kid names no key in the set.
	KeyNotFound = errs.Define(CodeKeyNotFound, "KEY_NOT_FOUND",
		"No key in the key set matches this token",
		"service/security/token: kid absent from the JWK Set",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyIDMissing is returned when a key-set verifier is handed a token with
	// no kid header. Selecting by id is the contract; trying every key in the
	// set instead would turn a large published JWKS into a work multiplier and
	// would quietly accept a token the publisher never routed to that key.
	KeyIDMissing = errs.Define(CodeKeyIDMissing, "KEY_ID_MISSING",
		"The token carries no key identifier",
		"service/security/token: set verifier requires a kid header; use a single-key verifier when tokens carry none",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyIDAmbiguous is returned when more keys carry the kid than the
	// verifier will try. jwk.Set refuses to pick between duplicate kids
	// because nothing distinguishes them; a signature does distinguish them,
	// so the engine tries the candidates — but a bounded number of them.
	KeyIDAmbiguous = errs.Define(CodeKeyIDAmbiguous, "KEY_ID_AMBIGUOUS",
		"Too many keys share this token key identifier",
		"service/security/token: candidate count past MaxKeyCandidates; a bounded rotation window is the supported case",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// FooterMismatch is returned for a PASETO footer that is not the expected
	// one, including a footer present where the verifier expects none. The
	// footer is authenticated but is not returned through the Verifier port,
	// so accepting an arbitrary one would authenticate data and then drop it.
	FooterMismatch = errs.Define(CodeFooterMismatch, "FOOTER_MISMATCH",
		"The token footer is not the expected one",
		"service/security/token: PASETO footer differs from the configured value, or is present with none configured",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// SchemeUnsupported is returned for a PASETO version+purpose the engine
	// does not implement — every local (encrypted) purpose, and every version
	// other than v4. It is returned instead of Malformed so an operator can
	// tell "this is not a token" from "this is a token I did not build for".
	SchemeUnsupported = errs.Define(CodeSchemeUnsupported, "SCHEME_UNSUPPORTED",
		"This token version and purpose are not supported",
		"service/security/token: only PASETO v4.public is implemented; v4.local needs primitives the SDK keeps in third-party",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// DuplicateMember is returned when a JOSE header or a claims object
	// carries the same member name twice. encoding/json would silently keep
	// the last, so a token could say one thing to this reader and another to
	// the next (RFC 8725 §2.6).
	DuplicateMember = errs.Define(CodeDuplicateMember, "DUPLICATE_MEMBER",
		"The token repeats a member name",
		"service/security/token: duplicate JSON member in the header or the claims object",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))
)
