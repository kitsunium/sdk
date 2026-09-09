package token_test

import (
	"strconv"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// epoch is the fixed instant every deterministic time test starts from.
var epoch = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

// TestRoundTripEveryAlgorithm pins that each of the three JWS bindings mints a
// token its own verifier accepts, and that the claims survive intact.
func TestRoundTripEveryAlgorithm(t *testing.T) {
	t.Parallel()
	ec, secret := testECKey(t), testSecret(t, 3)
	edPub, edPriv := testEdKey(t)
	manual := clock.NewManualClock(epoch)
	issuerCfg := svctoken.IssuerConfig{Issuer: "https://auth.example", Lifetime: time.Hour, Clock: manual}
	verifierCfg := svctoken.VerifierConfig{Issuer: "https://auth.example", Audience: "api", Clock: manual}

	makers := map[string]struct {
		issue  func() (coretoken.Issuer, error)
		verify func() (coretoken.Verifier, error)
	}{
		"HS256": {
			func() (coretoken.Issuer, error) { return svctoken.NewHS256Issuer(secret, issuerCfg) },
			func() (coretoken.Verifier, error) { return svctoken.NewHS256Verifier(secret, verifierCfg) },
		},
		"ES256": {
			func() (coretoken.Issuer, error) { return svctoken.NewES256Issuer(ec, issuerCfg) },
			func() (coretoken.Verifier, error) { return svctoken.NewES256Verifier(&ec.PublicKey, verifierCfg) },
		},
		"EdDSA": {
			func() (coretoken.Issuer, error) { return svctoken.NewEdDSAIssuer(edPriv, issuerCfg) },
			func() (coretoken.Verifier, error) { return svctoken.NewEdDSAVerifier(edPub, verifierCfg) },
		},
	}
	for name, maker := range makers {
		t.Run(name, func(t *testing.T) {
			issuer, err := maker.issue()
			if err != nil {
				t.Fatalf("issuer: %v", err)
			}
			verifier, err := maker.verify()
			if err != nil {
				t.Fatalf("verifier: %v", err)
			}
			minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("user-42").WithAudience("api"))
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			claims, err := verifier.Verify(minted)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if claims.Subject() != "user-42" || claims.Issuer() != "https://auth.example" {
				t.Fatalf("claims did not survive the round trip: %v", claims)
			}
			if !claims.Expiry().Equal(epoch.Add(time.Hour)) {
				t.Fatalf("exp = %v, want the configured lifetime from the injected clock", claims.Expiry())
			}
		})
	}
}

// TestExpiryIsDeterministicWithoutSleeping is the reason both configs take a
// clock. Time moves by assignment: the token is valid at issue, valid inside
// the leeway, and expired one nanosecond past it — asserted exactly, with no
// wall-clock tolerance and no sleeping.
func TestExpiryIsDeterministicWithoutSleeping(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 23), clock.NewManualClock(epoch)
	issuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{Leeway: 30 * time.Second, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(minted); verr != nil {
		t.Fatalf("at issue: %v", verr)
	}
	//: exactly at the far edge of the leeway, still valid.
	manual.Set(epoch.Add(time.Hour + 30*time.Second))
	if _, verr := verifier.Verify(minted); verr != nil {
		t.Fatalf("at exp+leeway: %v", verr)
	}
	//: one nanosecond past it, expired.
	manual.Advance(time.Nanosecond)
	if _, verr := verifier.Verify(minted); !errs.HasCode(verr, coretoken.CodeExpired) {
		t.Fatalf("past exp+leeway: got %v, want EXPIRED", verr)
	}
}

// TestNotBeforeIsDeterministic pins the same treatment for nbf.
func TestNotBeforeIsDeterministic(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 29), clock.NewManualClock(epoch)
	issuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{Lifetime: 2 * time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithNotBefore(epoch.Add(time.Hour)))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(minted); !errs.HasCode(verr, coretoken.CodeNotYetValid) {
		t.Fatalf("before nbf: got %v, want NOT_YET_VALID", verr)
	}
	manual.Set(epoch.Add(time.Hour))
	if _, verr := verifier.Verify(minted); verr != nil {
		t.Fatalf("at nbf: %v", verr)
	}
}

// TestExpiryIsRequiredOnBothSides pins the default that inverts RFC 7519
// §4.1.4's optionality — and pins it on the issuing side too, so a token this
// SDK mints can never be one this SDK would refuse.
func TestExpiryIsRequiredOnBothSides(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 31)
	strict, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	if _, ierr := strict.Issue(coretoken.NewClaimsValue().WithSubject("u")); !errs.HasCode(ierr, coretoken.CodeExpiryRequired) {
		t.Fatalf("issuing without an expiry: got %v, want EXPIRY_REQUIRED", ierr)
	}
	lax, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	eternal, err := lax.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	strictVerifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := strictVerifier.Verify(eternal); !errs.HasCode(verr, coretoken.CodeExpiryRequired) {
		t.Fatalf("verifying the eternal token: got %v, want EXPIRY_REQUIRED", verr)
	}
	laxVerifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := laxVerifier.Verify(eternal); verr != nil {
		t.Fatalf("the opt-out must work: %v", verr)
	}
}

// TestAudienceAndIssuerAreEnforced covers RFC 8725 §3.8 and §3.9: a correctly
// signed token addressed to another service, or minted by another issuer, is
// still not a valid token here.
func TestAudienceAndIssuerAreEnforced(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 37), clock.NewManualClock(epoch)
	issuer, err := svctoken.NewHS256Issuer(secret,
		svctoken.IssuerConfig{Issuer: "https://other.example", Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithAudience("billing", "reporting"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	wrongAudience, err := svctoken.NewHS256Verifier(secret,
		svctoken.VerifierConfig{Audience: "api", Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := wrongAudience.Verify(minted); !errs.HasCode(verr, coretoken.CodeAudienceMismatch) {
		t.Fatalf("wrong audience: got %v, want AUDIENCE_MISMATCH", verr)
	}
	wrongIssuer, err := svctoken.NewHS256Verifier(secret,
		svctoken.VerifierConfig{Issuer: "https://auth.example", Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := wrongIssuer.Verify(minted); !errs.HasCode(verr, coretoken.CodeIssuerMismatch) {
		t.Fatalf("wrong issuer: got %v, want ISSUER_MISMATCH", verr)
	}
	right, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{
		Issuer: "https://other.example", Audience: "reporting", Clock: manual,
	})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := right.Verify(minted); verr != nil {
		t.Fatalf("a matching audience anywhere in the array must pass: %v", verr)
	}
}

// TestMaxLifetimeRefusesLongCredentials pins the recipient-side cap, including
// its refusal to accept a lifetime it cannot measure.
func TestMaxLifetimeRefusesLongCredentials(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 41), clock.NewManualClock(epoch)
	issuer, err := svctoken.NewHS256Issuer(secret,
		svctoken.IssuerConfig{Lifetime: 30 * 24 * time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret,
		svctoken.VerifierConfig{MaxLifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(minted); !errs.HasCode(verr, coretoken.CodeLifetimeTooLong) {
		t.Fatalf("month-long token: got %v, want LIFETIME_TOO_LONG", verr)
	}
	//: an expiry with no issue time is a window that cannot be measured, and
	//: "I could not measure it" is not a reason to accept a credential. The
	//: token is hand-built because this issuer always stamps iat.
	noIAT := forge(`{"alg":"HS256","typ":"JWT"}`,
		`{"exp":`+strconv.FormatInt(epoch.Add(time.Minute).Unix(), 10)+`}`,
		hmacSigner(secret))
	if _, verr := verifier.Verify(noIAT); !errs.HasCode(verr, coretoken.CodeLifetimeTooLong) {
		t.Fatalf("token with no iat: got %v, want LIFETIME_TOO_LONG", verr)
	}
}

// TestPrivateClaimsSurviveVerbatim pins that an application claim comes back
// as the exact bytes the issuer signed — re-encoding a decoded value could
// change what was authenticated.
func TestPrivateClaimsSurviveVerbatim(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 43), clock.NewManualClock(epoch)
	claims, err := coretoken.NewClaimsValue().WithPrivateRaw("scope", []byte(`["read","write"]`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	claims, err = claims.WithPrivateRaw("tenant", []byte(`{"id":7,"tier":"gold"}`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	issuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := issuer.Issue(claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	verified, err := verifier.Verify(minted)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for name, want := range map[string]string{
		"scope":  `["read","write"]`,
		"tenant": `{"id":7,"tier":"gold"}`,
	} {
		got, found := verified.PrivateRaw(name)
		if !found || string(got) != want {
			t.Errorf("private claim %q = %q (found=%v), want %q", name, got, found, want)
		}
	}
}

// TestRequireTypeSeparatesTokenKinds covers RFC 8725 §3.11/§3.12: two kinds of
// token from one issuer need validation rules that reject each other's tokens.
func TestRequireTypeSeparatesTokenKinds(t *testing.T) {
	t.Parallel()
	secret, manual := testSecret(t, 47), clock.NewManualClock(epoch)
	access, err := svctoken.NewHS256Issuer(secret,
		svctoken.IssuerConfig{Type: "at+jwt", Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	minted, err := access.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	refreshVerifier, err := svctoken.NewHS256Verifier(secret,
		svctoken.VerifierConfig{RequireType: "rt+jwt", Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := refreshVerifier.Verify(minted); !errs.HasCode(verr, svctoken.CodeHeaderUnsupported) {
		t.Fatalf("access token at the refresh endpoint: got %v, want HEADER_UNSUPPORTED", verr)
	}
	accessVerifier, err := svctoken.NewHS256Verifier(secret,
		svctoken.VerifierConfig{RequireType: "at+jwt", Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := accessVerifier.Verify(minted); verr != nil {
		t.Fatalf("the matching kind must pass: %v", verr)
	}
}
