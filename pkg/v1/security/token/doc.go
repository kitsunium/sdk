// Package token — the dotted-quad codes a consumer routes on.
//
// These are RE-EXPORTS, not declarations: the ranges 0.2.13.* and 0.3.44.* are
// owned by internal/core/security/token, where every code of the token domain
// is declared (ADR 0160), and 0.3.42.* by internal/core/crypto/key/jwk; the
// errs ownership audit skips a cross-package selector for exactly this reason
// (ADR 0035). Matching on a code
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
//
// Package token — the algorithm, registered-claim and JWK key vocabulary.
//
// Package token — the constructor set, one per algorithm, plus the JWK
// loaders and bridges. Each constructor accepts only the Go key type its
// algorithm can use, which is what makes algorithm confusion a call that does
// not compile.
//
// Package token — the typed verdicts an issuer, a verifier or a key loader
// returns.
//
// Match them with errs.HasCode (the codes are re-exported in facade_gen.go) or
// errs.HasReason. Their Public halves name WHICH check refused a token and
// never echo a value out of it — not an audience, not an issuer, not a key id,
// not a claim. The JWK* verdicts are the key loaders' — ParseJWK and
// ParseJWKSet — and hold a key document to the same rule: they name the rule a
// member broke, never the member's value.
//
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
