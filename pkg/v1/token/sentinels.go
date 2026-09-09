// Package token — the typed verdicts an issuer or a verifier returns.
//
// Match them with errs.HasCode (the codes are re-exported in codes.go) or
// errs.HasReason. Their Public halves name WHICH check refused a token and
// never echo a value out of it — not an audience, not an issuer, not a key id,
// not a claim.
package token

import (
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

var (
	// Malformed is returned when the input cannot be read as a token.
	Malformed = coretoken.Malformed
	// AlgorithmNone is returned for a token declaring the unsecured "none"
	// algorithm. It is unconditional — no configuration accepts one.
	AlgorithmNone = coretoken.AlgorithmNone
	// AlgorithmMismatch is returned when the token's algorithm header is not
	// the algorithm the verifier is bound to. It is the verdict an
	// algorithm-confusion attempt produces, reached before any key is used.
	AlgorithmMismatch = coretoken.AlgorithmMismatch
	// SignatureInvalid is returned when the signature or MAC tag did not
	// authenticate under the bound key — the same verdict for a wrong key and
	// for tampered bytes, because they are the same event.
	SignatureInvalid = coretoken.SignatureInvalid
	// Expired is returned when an AUTHENTICATED token's exp is past.
	Expired = coretoken.Expired
	// NotYetValid is returned when an authenticated token's nbf is future.
	NotYetValid = coretoken.NotYetValid
	// ExpiryRequired is returned when a token carries no exp and the profile
	// requires one — the default on both the issuing and verifying side.
	ExpiryRequired = coretoken.ExpiryRequired
	// AudienceMismatch is returned when the configured audience is absent from
	// an authenticated token's aud (RFC 8725 §3.9).
	AudienceMismatch = coretoken.AudienceMismatch
	// IssuerMismatch is returned when an authenticated token's iss is not the
	// configured issuer (RFC 8725 §3.8).
	IssuerMismatch = coretoken.IssuerMismatch
	// TooLarge is returned for an input past a declared size bound, checked
	// before any parsing happens.
	TooLarge = coretoken.TooLarge
	// TooDeep is returned for claims nested past MaxClaimDepth.
	TooDeep = coretoken.TooDeep
	// KeyUnsuitable is returned when a key cannot serve the algorithm it was
	// handed to — the wrong length, curve, or key type.
	KeyUnsuitable = coretoken.KeyUnsuitable
	// PolicyMisconfigured is returned by a constructor given a configuration
	// it cannot honour. Permanent, not transient: the fix is at the
	// construction site, never a retry (ADR 0031).
	PolicyMisconfigured = coretoken.PolicyMisconfigured
	// IssueFailed is returned when an issuer cannot render the given claims —
	// a claim the target format cannot express, or a signing fault.
	IssueFailed = coretoken.IssueFailed
	// ClaimNameInvalid is returned for a private claim name that is empty or
	// shadows a registered one.
	ClaimNameInvalid = coretoken.ClaimNameInvalid
	// LifetimeTooLong is returned when an authenticated token's exp-iat span
	// exceeds VerifierConfig.MaxLifetime.
	LifetimeTooLong = coretoken.LifetimeTooLong

	// HeaderUnsupported is returned for a header carrying a non-empty "crit",
	// or a "typ" other than VerifierConfig.RequireType.
	HeaderUnsupported = svctoken.HeaderUnsupported
	// KeyNotFound is returned when a token's kid names no key in the set.
	KeyNotFound = svctoken.KeyNotFound
	// KeyIDMissing is returned when a set verifier is handed a token with no
	// kid header.
	KeyIDMissing = svctoken.KeyIDMissing
	// KeyIDAmbiguous is returned when more keys share a kid than
	// VerifierConfig.MaxKeyCandidates allows the verifier to try.
	KeyIDAmbiguous = svctoken.KeyIDAmbiguous
	// FooterMismatch is returned for a PASETO footer that is not the expected
	// one, including a footer present where none was configured.
	FooterMismatch = svctoken.FooterMismatch
	// SchemeUnsupported is returned for a PASETO version+purpose this package
	// does not implement — every local purpose, and every version but v4.
	SchemeUnsupported = svctoken.SchemeUnsupported
	// DuplicateMember is returned when a header or claims object repeats a
	// member name (RFC 8725 §2.6).
	DuplicateMember = svctoken.DuplicateMember
)
