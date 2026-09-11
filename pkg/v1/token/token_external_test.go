package token_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/token"
)

// The key the loaders are shown to load is RFC 7515 §A.3.1's ES256 key,
// published beside RFC 8037 §A.2's Ed25519 key — the vectors the internal jwk
// suite pins. A document somebody else wrote is the honest input for a loader:
// one this SDK rendered would be read back by the code that wrote it.
const (
	// rfc7515KID is the "kid" the tests publish the RFC 7515 key under; the
	// RFC prints the key without one.
	rfc7515KID = "rfc7515-a3"
	// rfc7515X is the RFC 7515 §A.3.1 x coordinate.
	rfc7515X = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"
	// rfc7515Y is the RFC 7515 §A.3.1 y coordinate.
	rfc7515Y = "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"
	// rfc7515D is the private scalar. It never reaches the facade: tokens are
	// signed with it through crypto/ecdsa directly, so a verification that
	// succeeds proves the PARSED public key is the RFC's key, rather than that
	// the SDK agrees with itself.
	rfc7515D = "jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI"
	// rfc7515PublicJWK is the public half as an issuer publishes it, with the
	// "use" and "alg" members a verifier built from it enforces.
	rfc7515PublicJWK = `{"kty":"EC","crv":"P-256","kid":"` + rfc7515KID + `",` +
		`"use":"sig","alg":"ES256","x":"` + rfc7515X + `","y":"` + rfc7515Y + `"}`
	// rfc8037PublicJWK sits beside it in the published set, so the set
	// verifier has to SELECT by kid rather than find the only member.
	rfc8037PublicJWK = `{"kty":"OKP","crv":"Ed25519","kid":"rfc8037-a2",` +
		`"x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`
)

// TestPublicRoundTrip walks the surface a consumer actually touches: build a
// key, mint, verify, read the claims back.
func TestPublicRoundTrip(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewES256Issuer(key, token.IssuerConfig{
		Issuer:   "https://auth.example",
		Lifetime: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	claims, err := token.SetPrivateClaim(
		token.NewClaims().WithSubject("user-42").WithAudience("api"),
		"scope", []string{"read", "write"})
	if err != nil {
		t.Fatalf("SetPrivateClaim: %v", err)
	}
	minted, err := issuer.Issue(claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := token.NewES256Verifier(&key.PublicKey, token.VerifierConfig{
		Issuer:   "https://auth.example",
		Audience: "api",
		Leeway:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewES256Verifier: %v", err)
	}
	verified, err := verifier.Verify(minted)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	scope, found, err := token.PrivateClaim[[]string](verified, "scope")
	if err != nil || !found || len(scope) != 2 || scope[0] != "read" {
		t.Fatalf("PrivateClaim = %v, %v, %v; want the two scopes", scope, found, err)
	}
}

// TestPrivateClaimReportsAbsenceWithoutAnError pins the three-result shape: a
// claim that was never sent is not an error, and a claim whose JSON does not
// fit the caller's type is.
func TestPrivateClaimReportsAbsenceWithoutAnError(t *testing.T) {
	t.Parallel()
	claims, err := token.SetPrivateClaim(token.NewClaims(), "count", 7)
	if err != nil {
		t.Fatalf("SetPrivateClaim: %v", err)
	}
	if _, found, perr := token.PrivateClaim[int](claims, "absent"); found || perr != nil {
		t.Fatalf("absent claim: found=%v err=%v, want false and no error", found, perr)
	}
	if _, found, perr := token.PrivateClaim[[]string](claims, "count"); !found || perr == nil {
		t.Fatalf("type mismatch: found=%v err=%v, want present and an error", found, perr)
	}
	value, found, perr := token.PrivateClaim[int](claims, "count")
	if !found || perr != nil || value != 7 {
		t.Fatalf("PrivateClaim = %v, %v, %v; want 7", value, found, perr)
	}
}

// TestSetPrivateClaimRefusesReservedNames pins that the public helper inherits
// the core value's rules rather than working around them.
func TestSetPrivateClaimRefusesReservedNames(t *testing.T) {
	t.Parallel()
	if _, err := token.SetPrivateClaim(token.NewClaims(), "exp", 1); !errs.HasCode(err, token.CodeClaimNameInvalid) {
		t.Fatalf("reserved name: got %v, want CLAIM_NAME_INVALID", err)
	}
}

// TestSentinelsAreMatchable pins that a consumer can route on the verdicts,
// which is the whole reason they are exported.
func TestSentinelsAreMatchable(t *testing.T) {
	t.Parallel()
	secret, err := crypto.NewKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	verifier, err := token.NewHS256Verifier(secret, token.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	_, verr := verifier.Verify("not.a.token")
	if !errs.HasCode(verr, token.CodeMalformed) {
		t.Fatalf("garbage: got %v, want MALFORMED", verr)
	}
	if errs.HTTPStatusOf(verr) != 401 {
		t.Fatalf("HTTP status = %d, want 401", errs.HTTPStatusOf(verr))
	}
}

// TestAlgorithmNamesAreThePublicWireNames guards the constants a consumer
// might compare against a header they logged.
func TestAlgorithmNamesAreThePublicWireNames(t *testing.T) {
	t.Parallel()
	for alg, want := range map[token.Algorithm]string{
		token.AlgorithmHS256:          "HS256",
		token.AlgorithmES256:          "ES256",
		token.AlgorithmEdDSA:          "EdDSA",
		token.AlgorithmPasetoV4Public: "v4.public",
	} {
		if got := alg.String(); got != want {
			t.Errorf("%v.String() = %q, want %q", alg, got, want)
		}
	}
}

// TestAPublishedKeyVerifiesThroughThePublicAPI walks a relying party's whole
// path to an issuer's published key with nothing but pkg/v1: parse the JWK and
// the JWK Set documents, build both key-based verifiers from what was parsed,
// verify an ES256 token with each — and refuse one signed by a key the issuer
// never published, so that "verified" means the parsed key did the work.
//
// That path did not exist. NewVerifierFromJWK and NewSetVerifier shipped
// taking a JWK and a JWKSet — aliases of internal types whose fields are all
// unexported — while every function able to build one lived under internal/,
// which Go forbids a consumer to import. The facade's own wiring test built
// its keys by importing that package, and so hid it. This file imports nothing
// under internal/, so removing the loaders makes it stop COMPILING.
//
// MUTATION (2026-09-11): ParseJWK was made to return `JWK{}, nil` — a loader
// that answers every document with an inert key. Observed: `construct from the
// parsed key: [0.2.13.12 KEY_UNSUITABLE] The key cannot be used for this token
// algorithm`, on the NewVerifierFromJWK row; the set row, which never calls
// ParseJWK, stayed green. Restored; constructors.go's SHA-256 is
// byte-identical to the pre-mutation one.
//
// MUTATION (2026-09-11): ParseJWKSet was made to return `JWKSet{}, nil`.
// Observed: `construct from the parsed key: [0.2.13.13 POLICY_MISCONFIGURED]
// The token policy is misconfigured`, on the NewSetVerifier row. Restored the
// same way.
func TestAPublishedKeyVerifiesThroughThePublicAPI(t *testing.T) {
	t.Parallel()
	key, err := token.ParseJWK([]byte(rfc7515PublicJWK))
	if err != nil {
		t.Fatalf("ParseJWK(RFC 7515 §A.3.1): %v", err)
	}
	set, err := token.ParseJWKSet([]byte(`{"keys":[` + rfc8037PublicJWK + `,` + rfc7515PublicJWK + `]}`))
	if err != nil {
		t.Fatalf("ParseJWKSet: %v", err)
	}
	genuine := mintES256(t, rfc7515Signer(t))
	stranger, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	forged := mintES256(t, stranger)

	for name, build := range map[string]func() (token.Verifier, error){
		"NewVerifierFromJWK(ParseJWK)": func() (token.Verifier, error) {
			return token.NewVerifierFromJWK(key, token.VerifierConfig{})
		},
		"NewSetVerifier(ParseJWKSet)": func() (token.Verifier, error) {
			return token.NewSetVerifier(set, token.VerifierConfig{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			verifier, berr := build()
			if berr != nil {
				t.Fatalf("construct from the parsed key: %v", berr)
			}
			claims, verr := verifier.Verify(genuine)
			if verr != nil || claims.Subject() != "user-42" {
				t.Fatalf("token signed by the published key: subject %q, err %v; want user-42 and no error",
					claims.Subject(), verr)
			}
			if _, ferr := verifier.Verify(forged); !errs.HasCode(ferr, token.CodeSignatureInvalid) {
				t.Fatalf("token signed by an unpublished key: got %v, want SIGNATURE_INVALID", ferr)
			}
		})
	}
}

// TestAMalformedJWKIsRefusedWithAMatchableCode pins that every refusal the
// loaders return can be routed on from pkg/v1 alone — by code through
// errs.HasCode and by sentinel through errors.Is — with one row per
// re-exported code, because a re-export is a one-liner and a one-liner naming
// the wrong internal constant still compiles.
//
// Each bad document is also published as the SECOND member of a set whose
// first member is valid, and the set must be refused with the bad member's own
// code: a set is accepted whole or not at all, never loaded short of a key.
//
// MUTATION (2026-09-11): CodeJWKKeyMismatch was re-pointed at
// jwk.CodeJWKInvalidEncoding, which compiles. Observed, on the off-curve row
// only: `ParseJWK: got [0.3.42.6 KEY_MISMATCH] JWK material does not match the
// declared curve, want errs.HasCode(err, 0.3.42.5)`. The same re-pointing of
// the JWKKeyMismatch sentinel was caught by the other half: `want
// errors.Is(err, [0.3.42.5 INVALID_ENCODING] JWK member is not valid unpadded
// base64url)`. Both restored; codes.go and sentinels.go are byte-identical to
// the pre-mutation files by SHA-256.
//
// MUTATION (2026-09-11), made in internal/service/crypto/jwk because that is
// where a lenient set decoder would live: parseMembers was made to skip a
// refused member instead of refusing the document. Observed, on every row:
// `ParseJWKSet kept 1 key(s) out of a refused document`. Restored; set.go's
// SHA-256 is byte-identical to the pre-mutation one.
func TestAMalformedJWKIsRefusedWithAMatchableCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		document string
		code     errs.Code
		sentinel error
	}{
		{
			"a JSON array where a key object belongs", `["EC"]`,
			token.CodeJWKMalformed, token.JWKMalformed,
		},
		{
			//: json.Unmarshal takes null into a struct and leaves it zero, so
			//: this read as a key missing its kty, in a key and in a set alike.
			"JSON null where a key object belongs", `null`,
			token.CodeJWKMalformed, token.JWKMalformed,
		},
		{
			"an EC key without y", `{"kty":"EC","crv":"P-256","x":"` + rfc7515X + `"}`,
			token.CodeJWKMissingMember, token.JWKMissingMember,
		},
		{
			"an RSA key", `{"kty":"RSA","n":"AQAB","e":"AQAB"}`,
			token.CodeJWKUnsupportedKeyType, token.JWKUnsupportedKeyType,
		},
		{
			"P-256 under kty OKP", `{"kty":"OKP","crv":"P-256","x":"` + rfc7515X + `"}`,
			token.CodeJWKUnsupportedCurve, token.JWKUnsupportedCurve,
		},
		{
			"a padded base64url coordinate", `{"kty":"EC","crv":"P-256","x":"` + rfc7515X + `=","y":"` + rfc7515Y + `"}`,
			token.CodeJWKInvalidEncoding, token.JWKInvalidEncoding,
		},
		{
			"a point off the curve", `{"kty":"EC","crv":"P-256","x":"` + rfc7515X + `","y":"` + rfc7515X + `"}`,
			token.CodeJWKKeyMismatch, token.JWKKeyMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, err := token.ParseJWK([]byte(tc.document))
			expectRoutable(t, "ParseJWK", err, tc.code, tc.sentinel)
			if !key.IsZero() {
				t.Errorf("ParseJWK returned a non-zero JWK beside its refusal: %v", key)
			}
			set, serr := token.ParseJWKSet([]byte(`{"keys":[` + rfc7515PublicJWK + `,` + tc.document + `]}`))
			expectRoutable(t, "ParseJWKSet(valid, bad)", serr, tc.code, tc.sentinel)
			if set.Len() != 0 {
				t.Errorf("ParseJWKSet kept %d key(s) out of a refused document", set.Len())
			}
		})
	}
}

// TestANullJWKSetEnvelopeIsMalformed pins the envelope half: a JWK Set
// document that is JSON null is not a set whose keys are missing, it is not an
// object at all. It read as JWKMissingMember before.
func TestANullJWKSetEnvelopeIsMalformed(t *testing.T) {
	t.Parallel()
	set, err := token.ParseJWKSet([]byte(`null`))
	expectRoutable(t, "ParseJWKSet(null)", err, token.CodeJWKMalformed, token.JWKMalformed)
	if set.Len() != 0 {
		t.Errorf("ParseJWKSet(null) kept %d key(s)", set.Len())
	}
}

// expectRoutable reports, separately, whether err answers to code through
// errs.HasCode and to sentinel through errors.Is — the two ways a consumer
// routes on a refusal. Each is re-exported on its own line, so each can be
// wrong on its own, and a failure has to say which one it was.
func expectRoutable(t *testing.T, call string, err error, code errs.Code, sentinel error) {
	t.Helper()
	if !errs.HasCode(err, code) {
		t.Errorf("%s: got %v, want errs.HasCode(err, %v)", call, err, code)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("%s: got %v, want errors.Is(err, %v)", call, err, sentinel)
	}
}

// rfc7515Signer rebuilds RFC 7515 §A.3.1's private key with crypto/ecdsa
// alone, from the scalar the RFC prints.
func rfc7515Signer(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	scalar, err := base64.RawURLEncoding.DecodeString(rfc7515D)
	if err != nil {
		t.Fatalf("decode the RFC scalar: %v", err)
	}
	signer, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
	if err != nil {
		t.Fatalf("ParseRawPrivateKey: %v", err)
	}
	return signer
}

// mintES256 issues a token for user-42 under rfc7515KID, signed by signer.
func mintES256(t *testing.T, signer *ecdsa.PrivateKey) string {
	t.Helper()
	issuer, err := token.NewES256Issuer(signer, token.IssuerConfig{Lifetime: time.Hour, KeyID: rfc7515KID})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	minted, err := issuer.Issue(token.NewClaims().WithSubject("user-42"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return minted
}
