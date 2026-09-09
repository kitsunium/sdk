package token_test

import (
	"strings"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// pasetoPair builds an issuer/verifier pair over one fresh Ed25519 keypair.
func pasetoPair(t *testing.T, issue svctoken.PasetoIssuerConfig, verify svctoken.PasetoVerifierConfig) (coretoken.Issuer, coretoken.Verifier) {
	t.Helper()
	pub, priv := testEdKey(t)
	issuer, err := svctoken.NewPasetoV4Issuer(priv, issue)
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	verifier, err := svctoken.NewPasetoV4Verifier(pub, verify)
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	return issuer, verifier
}

// TestPasetoV4PublicRoundTrip pins the format's shape as well as its claims:
// a v4.public token always begins with the version+purpose header, and that
// header is part of what the signature covers.
func TestPasetoV4PublicRoundTrip(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	issuer, verifier := pasetoPair(t,
		svctoken.PasetoIssuerConfig{
			Issuer: "https://auth.example", Lifetime: time.Hour, Clock: manual,
		},
		svctoken.PasetoVerifierConfig{
			Issuer: "https://auth.example", Audience: "api", Clock: manual,
		})
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("user-42").WithAudience("api"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(minted, "v4.public.") {
		t.Fatalf("token = %q, want the v4.public header", minted)
	}
	if strings.Count(minted, ".") != 2 {
		t.Fatalf("a footerless token must have three segments, got %q", minted)
	}
	claims, err := verifier.Verify(minted)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject() != "user-42" || !claims.Expiry().Equal(epoch.Add(time.Hour)) {
		t.Fatalf("claims did not survive the round trip: %v", claims)
	}
}

// TestPasetoUsesRFC3339Timestamps pins the encoding difference that makes
// PASETO a second claim shape rather than a second framing of the first one.
func TestPasetoUsesRFC3339Timestamps(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	issuer, _ := pasetoPair(t,
		svctoken.PasetoIssuerConfig{Lifetime: time.Hour, Clock: manual},
		svctoken.PasetoVerifierConfig{Clock: manual})
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	body := strings.Split(minted, ".")[2]
	raw, derr := b64.DecodeString(body)
	if derr != nil {
		t.Fatalf("DecodeString: %v", derr)
	}
	//: the signature is appended to the payload, so the JSON is a prefix.
	if !strings.Contains(string(raw), `"exp":"2030-01-01T01:00:00Z"`) {
		t.Fatalf("payload does not carry an RFC 3339 exp: %q", raw[:min(len(raw), 200)])
	}
}

// TestPasetoRefusesMultiValuedAudience pins that an unrepresentable claim
// refuses the mint rather than being quietly truncated — a token whose
// audience silently lost a value would pass an audience check the caller never
// intended to grant.
func TestPasetoRefusesMultiValuedAudience(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	issuer, _ := pasetoPair(t,
		svctoken.PasetoIssuerConfig{Lifetime: time.Hour, Clock: manual},
		svctoken.PasetoVerifierConfig{Clock: manual})
	_, err := issuer.Issue(coretoken.NewClaimsValue().WithAudience("a", "b"))
	if !errs.HasCode(err, coretoken.CodeIssueFailed) {
		t.Fatalf("two audiences: got %v, want ISSUE_FAILED", err)
	}
}

// TestPasetoFooterIsAuthenticatedAndExpected pins both halves of the footer
// rule: the configured footer round-trips, and an unexpected one is refused
// rather than authenticated and dropped.
func TestPasetoFooterIsAuthenticatedAndExpected(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	footer := []byte(`{"kid":"2030-01"}`)
	issuer, verifier := pasetoPair(t,
		svctoken.PasetoIssuerConfig{
			Lifetime: time.Hour, Clock: manual,
			Footer: footer,
		},
		svctoken.PasetoVerifierConfig{
			Clock:  manual,
			Footer: footer,
		})
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if strings.Count(minted, ".") != 3 {
		t.Fatalf("a footered token must have four segments, got %q", minted)
	}
	if _, verr := verifier.Verify(minted); verr != nil {
		t.Fatalf("matching footer: %v", verr)
	}
	pub, priv := testEdKey(t)
	//: the same issuer, a verifier expecting no footer.
	strictIssuer, err := svctoken.NewPasetoV4Issuer(priv, svctoken.PasetoIssuerConfig{
		Lifetime: time.Hour, Clock: manual,
		Footer: footer,
	})
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	footered, err := strictIssuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	noFooter, err := svctoken.NewPasetoV4Verifier(pub, svctoken.PasetoVerifierConfig{
		Clock: manual,
	})
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	if _, verr := noFooter.Verify(footered); !errs.HasCode(verr, svctoken.CodeFooterMismatch) {
		t.Fatalf("unexpected footer: got %v, want FOOTER_MISMATCH", verr)
	}
}

// TestPasetoImplicitAssertionMustMatch pins v4's out-of-band context: it is
// signed but never transmitted, so a token only verifies for a recipient that
// already knows it.
func TestPasetoImplicitAssertionMustMatch(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	pub, priv := testEdKey(t)
	issuer, err := svctoken.NewPasetoV4Issuer(priv, svctoken.PasetoIssuerConfig{
		Lifetime: time.Hour, Clock: manual,
		ImplicitAssertion: []byte("tenant-7"),
	})
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	matching, err := svctoken.NewPasetoV4Verifier(pub, svctoken.PasetoVerifierConfig{
		Clock:             manual,
		ImplicitAssertion: []byte("tenant-7"),
	})
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	if _, verr := matching.Verify(minted); verr != nil {
		t.Fatalf("matching implicit assertion: %v", verr)
	}
	other, err := svctoken.NewPasetoV4Verifier(pub, svctoken.PasetoVerifierConfig{
		Clock:             manual,
		ImplicitAssertion: []byte("tenant-8"),
	})
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	if _, verr := other.Verify(minted); !errs.HasCode(verr, coretoken.CodeSignatureInvalid) {
		t.Fatalf("wrong implicit assertion: got %v, want SIGNATURE_INVALID", verr)
	}
}

// TestUnsupportedPasetoSchemesAreNamed pins that a recognisable PASETO of
// another version or purpose — v4.local above all — gets a verdict saying so.
// "Malformed" would send an operator looking for a corrupt token instead of a
// missing feature.
func TestUnsupportedPasetoSchemesAreNamed(t *testing.T) {
	t.Parallel()
	_, verifier := pasetoPair(t,
		svctoken.PasetoIssuerConfig{AllowMissingExpiry: true},
		svctoken.PasetoVerifierConfig{AllowMissingExpiry: true})
	for _, scheme := range []string{"v4.local", "v3.local", "v2.public", "v1.local"} {
		_, verr := verifier.Verify(scheme + "." + b64.EncodeToString([]byte("payload")))
		if !errs.HasCode(verr, svctoken.CodeSchemeUnsupported) {
			t.Errorf("%s: got %v, want SCHEME_UNSUPPORTED", scheme, verr)
		}
	}
	for _, notAToken := range []string{"v9.public.aaaa", "x4.public.aaaa", "aa.bb.cc"} {
		_, verr := verifier.Verify(notAToken)
		if !errs.HasCode(verr, coretoken.CodeMalformed) {
			t.Errorf("%q: got %v, want MALFORMED", notAToken, verr)
		}
	}
}

// TestPasetoTamperingIsRefused pins that the pre-authentication encoding binds
// the payload: flipping one character of the body breaks the signature.
func TestPasetoTamperingIsRefused(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	issuer, verifier := pasetoPair(t,
		svctoken.PasetoIssuerConfig{Lifetime: time.Hour, Clock: manual},
		svctoken.PasetoVerifierConfig{Clock: manual})
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	tampered := minted[:len(minted)-2] + nextAlphabetChar(minted[len(minted)-2]) + minted[len(minted)-1:]
	if _, verr := verifier.Verify(tampered); !errs.HasCode(verr, coretoken.CodeSignatureInvalid) {
		t.Fatalf("tampered token: got %v, want SIGNATURE_INVALID", verr)
	}
	//: a body shorter than the signature is a structural failure, not a
	//: verification one — and must not index out of range on the way there.
	if _, verr := verifier.Verify("v4.public." + b64.EncodeToString([]byte("short"))); !errs.HasCode(verr, coretoken.CodeMalformed) {
		t.Fatalf("truncated body: got %v, want MALFORMED", verr)
	}
}
