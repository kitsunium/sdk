// Package token — declares the sentinels specific to the two concrete token
// formats implemented here. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// As in core/token, the Public half names the check that refused the token and
// never echoes a value out of it — not a kid, not a footer, not a claim.
package token

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65) — the input was not readable.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77) — the token was read and refused.
const exitNoPerm int = 77

// exitConfigFault matches sysexits EX_CONFIG (78), the exit code core/token's
// KeyUnsuitable / PolicyMisconfigured / IssueFailed sentinels carry. It is
// restated here so a stdlib cause wrapped through WrapParams — the one path
// that cannot inherit an exit override — lands on the same number its sibling
// sentinel would have produced.
const exitConfigFault int = 78

// statusUnauthorized is HTTP 401, for the same reason core/token uses it: a
// refused credential is answered by obtaining a new one, not by editing the
// request (RFC 6750 §3.1).
const statusUnauthorized int = 401

var (
	// HeaderUnsupported is returned for a JOSE header this package will not
	// act on: a non-empty "crit", or a "typ" other than the required one.
	//
	// A non-empty "crit" is a rejection and not a warning because RFC 7515
	// §4.1.11 says so: the parameters it names MUST be understood, this
	// package understands none of them, and "ignore what you do not
	// understand" is how a security-relevant extension gets silently dropped.
	HeaderUnsupported = errs.Define(CodeHeaderUnsupported, "HEADER_UNSUPPORTED",
		"The token header carries parameters this verifier will not accept",
		"service/token: non-empty crit, or typ differs from the required value",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyNotFound is returned when the token's kid names no key in the set.
	KeyNotFound = errs.Define(CodeKeyNotFound, "KEY_NOT_FOUND",
		"No key in the key set matches this token",
		"service/token: kid absent from the JWK Set",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyIDMissing is returned when a key-set verifier is handed a token with
	// no kid header. Selecting by id is the contract; trying every key in the
	// set instead would turn a large published JWKS into a work multiplier and
	// would quietly accept a token the publisher never routed to that key.
	KeyIDMissing = errs.Define(CodeKeyIDMissing, "KEY_ID_MISSING",
		"The token carries no key identifier",
		"service/token: set verifier requires a kid header; use a single-key verifier when tokens carry none",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// KeyIDAmbiguous is returned when more keys carry the kid than the
	// verifier will try. jwk.Set refuses to pick between duplicate kids
	// because nothing distinguishes them; a signature does distinguish them,
	// so this package tries the candidates — but a bounded number of them.
	KeyIDAmbiguous = errs.Define(CodeKeyIDAmbiguous, "KEY_ID_AMBIGUOUS",
		"Too many keys share this token key identifier",
		"service/token: candidate count past MaxKeyCandidates; a bounded rotation window is the supported case",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// FooterMismatch is returned for a PASETO footer that is not the expected
	// one, including a footer present where the verifier expects none. The
	// footer is authenticated but is not returned through the Verifier port,
	// so accepting an arbitrary one would authenticate data and then drop it.
	FooterMismatch = errs.Define(CodeFooterMismatch, "FOOTER_MISMATCH",
		"The token footer is not the expected one",
		"service/token: PASETO footer differs from the configured value, or is present with none configured",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// SchemeUnsupported is returned for a PASETO version+purpose this package
	// does not implement — every local (encrypted) purpose, and every version
	// other than v4. It is returned instead of Malformed so an operator can
	// tell "this is not a token" from "this is a token I did not build for".
	SchemeUnsupported = errs.Define(CodeSchemeUnsupported, "SCHEME_UNSUPPORTED",
		"This token version and purpose are not supported",
		"service/token: only PASETO v4.public is implemented; v4.local needs primitives the SDK keeps in third-party",
		errs.WithExitCode(exitNoPerm), errs.WithHTTPStatus(statusUnauthorized))

	// DuplicateMember is returned when a JOSE header or a claims object
	// carries the same member name twice. encoding/json would silently keep
	// the last, so a token could say one thing to this reader and another to
	// the next (RFC 8725 §2.6).
	DuplicateMember = errs.Define(CodeDuplicateMember, "DUPLICATE_MEMBER",
		"The token repeats a member name",
		"service/token: duplicate JSON member in the header or the claims object",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(statusUnauthorized))
)
