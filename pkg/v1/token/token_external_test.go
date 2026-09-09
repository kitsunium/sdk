package token_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/token"
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
