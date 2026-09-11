//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/token .

// Package token is the public facade for the SDK's security-token domain:
// JSON Web Tokens over JWS Compact Serialization (RFC 7519) and PASETO
// v4.public. It is stdlib-only and composes the SDK's own crypto schemes, so
// it adds no dependency to a consumer's build.
//
//	iss, err := token.NewES256Issuer(privateKey, token.IssuerConfig{
//	    Issuer:   "https://auth.example.com",
//	    Lifetime: 15 * time.Minute,
//	})
//	jwt, err := iss.Issue(token.NewClaims().WithSubject("user-42").WithAudience("api"))
//
//	ver, err := token.NewES256Verifier(&privateKey.PublicKey, token.VerifierConfig{
//	    Issuer:   "https://auth.example.com",
//	    Audience: "api",
//	    Leeway:   30 * time.Second,
//	})
//	claims, err := ver.Verify(jwt)
//
// # Algorithm confusion is a call you cannot write
//
// There is no NewVerifier(algorithm, key), and no verifier reads the token's
// "alg" header to decide what to do with it. There is one constructor per
// algorithm, and each accepts only the Go type that algorithm can use:
//
//	NewHS256Verifier(secret crypto.Key,      cfg VerifierConfig)
//	NewES256Verifier(pub *ecdsa.PublicKey,   cfg VerifierConfig)
//	NewEdDSAVerifier(pub ed25519.PublicKey,  cfg VerifierConfig)
//
// The textbook attack — take the RSA or EC public key the server publishes,
// use it as an HMAC shared secret, mint tokens with it, and let a verifier
// that trusts the header do the rest — needs a call that hands a public key to
// the HMAC path. Here that call does not compile: crypto.Key is a struct with
// an unexported field, and no *ecdsa.PublicKey converts to it. Verification
// then COMPARES the header against the binding and refuses a mismatch with
// AlgorithmMismatch, before any key reaches any primitive.
//
// [NewVerifierFromJWK] and [NewSetVerifier] are the only places an algorithm
// is selected at run time, and they select it from the KEY's kty/crv — the
// material a relying party fetched from a publisher it trusts — never from the
// token.
//
// # Loading a published key
//
// A relying party that verifies against an issuer's published keys fetches the
// key document itself — this package performs no I/O, follows no jwks_uri and
// caches nothing (ADR 0042) — and hands the bytes to [ParseJWKSet], or to
// [ParseJWK] for a single key:
//
//	set, err := token.ParseJWKSet(body) // the issuer's JWK Set, as fetched
//	ver, err := token.NewSetVerifier(set, token.VerifierConfig{
//	    Issuer:   "https://auth.example.com",
//	    Audience: "api",
//	})
//
// Parsing is the only way to obtain a [JWK]: the type has no exported field
// and no UnmarshalJSON, so its validation — strict base64url, coordinates at
// the curve's fixed length, the point on its curve, a private "d" deriving
// exactly the declared public key — is not a step a caller can skip by
// decoding some other way. [NewJWKSet] assembles a set from keys already
// parsed. A refusal carries one of the CodeJWK* codes, matchable with
// errs.HasCode; a set is accepted whole or not at all, so one RSA member —
// the SDK verifies nothing with RSA — refuses the document.
//
// # What is always refused
//
//   - "alg":"none" in any capitalisation (RFC 7519 §6). [Algorithm] is an enum
//     with no value that spells it, so no configuration can enable it.
//   - a non-empty "crit" header (RFC 7515 §4.1.11).
//   - a token with no "exp", unless VerifierConfig.AllowMissingExpiry says
//     otherwise. RFC 7519 §4.1.4 makes exp OPTIONAL; a bearer token that never
//     expires is a password with a worse rotation story, so the default here
//     inverts that and the opt-out has a name.
//   - a token past MaxTokenLen, or claims past MaxClaimDepth — both checked
//     before any decode allocates.
//   - a repeated JSON member in the header or the claims (RFC 8725 §2.6).
//
// # Testing time without sleeping
//
// Every config has a Clock field, and it takes any value with the two methods
// Now and Since — so the fake is one a test writes itself, and an expiry test
// moves time by assignment instead of by sleeping:
//
//	type fakeClock struct{ now time.Time }
//
//	func (c *fakeClock) Now() time.Time                  { return c.now }
//	func (c *fakeClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
//
//	fake := &fakeClock{now: time.Unix(1000, 0)}
//	ver, _ := token.NewHS256Verifier(secret, token.VerifierConfig{Clock: fake})
//	fake.now = fake.now.Add(2 * time.Hour) // now the token is expired, deterministically
//
// Move it between calls, or guard it with a mutex if a verifier reads it from
// another goroutine at the same time.
//
// # Claims are not printable
//
// [Claims] renders its SHAPE under %v / %s / %#v — "token.Claims{registered:3
// aud:1 private:2}" — and never a value. A claim set is the output of an
// authentication decision and routinely holds a subject id, an email or a
// tenant; the counts are enough to tell "no audience was sent" from "the
// audience did not match", which is what a %v is usually reaching for.
package token

import (
	"encoding/json"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// Algorithm is the public alias for the signature algorithm a token issuer or
// verifier is bound to.
type Algorithm = coretoken.Algorithm

// Claims is the public alias for a token's immutable claim set.
type Claims = coretoken.ClaimsValue

// Issuer is the public alias for the token-minting port.
type Issuer = coretoken.Issuer

// Verifier is the public alias for the token-authenticating port.
type Verifier = coretoken.Verifier

// IssuerConfig is the public alias for the JWT issuing policy.
type IssuerConfig = svctoken.IssuerConfig

// VerifierConfig is the public alias for the JWT verification policy.
type VerifierConfig = svctoken.VerifierConfig

// PasetoIssuerConfig is the public alias for the PASETO v4.public issuing
// policy. It is a separate type rather than [IssuerConfig] plus two extras
// because PASETO has no header parameters: there is nothing for a Type or a
// KeyID to set, and publishing knobs the format ignores is how a configuration
// field ends up doing nothing. Its equivalent of a "kid" is the Footer, which
// the signature covers.
type PasetoIssuerConfig = svctoken.PasetoIssuerConfig

// PasetoVerifierConfig is the public alias for the PASETO v4.public
// verification policy. Same shape decision as its issuing counterpart: no
// RequireType, no MaxKeyCandidates, because PASETO has neither a "typ" to
// require nor a "kid" to select on.
type PasetoVerifierConfig = svctoken.PasetoVerifierConfig

// NewClaims returns an empty claim set to build on with the With* methods.
func NewClaims() Claims {
	//: delegate to the core constructor.
	return coretoken.NewClaimsValue()
}

// PrivateClaim decodes the named application claim of claims into T.
//
// It lives here rather than on the Claims value because "what shape is a scope
// claim" is the caller's question, not the domain's: the core value stores
// private claims as the exact JSON bytes the issuer signed, so re-encoding a
// decoded value can never change what was authenticated.
//
// The second result reports presence, which is how a claim explicitly set to
// JSON null is told apart from one that was never sent.
func PrivateClaim[T any](claims Claims, name string) (value T, found bool, err error) {
	raw, present := claims.PrivateRaw(name)
	//: absence is a normal answer, not an error.
	if !present {
		//: the zero T, and a false that says why.
		return value, false, nil
	}
	//: a decode failure is the caller's type disagreeing with the token.
	if uerr := json.Unmarshal(raw, &value); uerr != nil {
		//: hand back the stdlib cause; the claim was present.
		return value, true, uerr
	}
	//: decoded.
	return value, true, nil
}

// SetPrivateClaim returns a copy of claims carrying name encoded as JSON. It
// refuses an empty name, one of the seven registered names, and a claim set
// already at its private-claim cap.
func SetPrivateClaim[T any](claims Claims, name string, value T) (updated Claims, err error) {
	raw, merr := json.Marshal(value)
	//: a value the encoder cannot render is a caller error.
	if merr != nil {
		//: hand back the stdlib cause.
		return Claims{}, merr
	}
	//: the core value enforces the name and count rules.
	return claims.WithPrivateRaw(name, raw)
}
