package token_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestNoAlgorithmSpellsNone is the structural half of the "alg:none is
// refused" promise: the refusal cannot be configured away because there is no
// Algorithm value that renders the string a "none" header would carry. The
// scan covers the whole uint8 range, so adding a constant that spells it —
// however it is spelled — fails here rather than in production.
func TestNoAlgorithmSpellsNone(t *testing.T) {
	t.Parallel()
	for candidate := range 256 {
		alg := token.Algorithm(candidate)
		if strings.EqualFold(alg.String(), "none") {
			t.Fatalf("Algorithm(%d) renders %q, which a none header would match", candidate, alg.String())
		}
	}
}

// TestKnownMatchesTheRenderedSet pins that Known() and String() agree: every
// Algorithm that renders a real wire name is Known, and every one that is not
// Known renders the sentinel "unknown" — which matches no header, so a zero
// value can never be mistaken for a binding.
func TestKnownMatchesTheRenderedSet(t *testing.T) {
	t.Parallel()
	for candidate := range 256 {
		alg := token.Algorithm(candidate)
		rendered, known := alg.String(), alg.Known()
		switch {
		case known && rendered == "unknown":
			t.Fatalf("Algorithm(%d) is Known but renders the sentinel name", candidate)
		case !known && rendered != "unknown":
			t.Fatalf("Algorithm(%d) is not Known yet renders %q", candidate, rendered)
		}
	}
}

// TestAlgorithmWireNames pins the JOSE and PASETO spellings. A typo here would
// make every token this SDK issues unverifiable by anybody else, and every
// token anybody else issues unverifiable here — silently, since the comparison
// would simply always fail.
func TestAlgorithmWireNames(t *testing.T) {
	t.Parallel()
	for alg, want := range map[token.Algorithm]string{
		token.AlgorithmHS256:          "HS256",
		token.AlgorithmES256:          "ES256",
		token.AlgorithmEdDSA:          "EdDSA",
		token.AlgorithmPasetoV4Public: "v4.public",
		token.AlgorithmUnknown:        "unknown",
	} {
		if got := alg.String(); got != want {
			t.Errorf("Algorithm(%d).String() = %q, want %q", alg, got, want)
		}
	}
}

// TestClaimsNeverPrintAValue is the redaction guard. It builds a claim set
// where every field holds a distinctive marker, then checks that no verb fmt
// offers puts any marker on the page. Without ClaimsValue.String and GoString,
// %#v walks the unexported fields reflectively and dumps all of them — that is
// not hypothetical, it is what this test catches when the methods are removed.
func TestClaimsNeverPrintAValue(t *testing.T) {
	t.Parallel()
	claims, err := token.NewClaimsValue().
		WithIssuer("MARKER-ISS").
		WithSubject("MARKER-SUB").
		WithID("MARKER-JTI").
		WithAudience("MARKER-AUD").
		WithPrivateRaw("role", []byte(`"MARKER-ROLE"`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	for _, verb := range []string{"%v", "%s", "%#v", "%+v"} {
		rendered := fmt.Sprintf(verb, claims)
		if strings.Contains(rendered, "MARKER") {
			t.Fatalf("%s rendered a claim value: %q", verb, rendered)
		}
		if !strings.Contains(rendered, "token.Claims{") {
			t.Fatalf("%s did not render the shape summary: %q", verb, rendered)
		}
	}
}

// TestClaimsShapeSummaryCounts pins the counts the redacted rendering does
// report, since they are what makes it useful: "no audience was sent" and "the
// audience did not match" are different bugs.
func TestClaimsShapeSummaryCounts(t *testing.T) {
	t.Parallel()
	claims := token.NewClaimsValue().
		WithIssuer("a").WithSubject("b").
		WithAudience("x", "y").
		WithExpiry(time.Unix(100, 0))
	got := claims.String()
	if !strings.Contains(got, "registered:4") || !strings.Contains(got, "aud:2") {
		t.Fatalf("shape summary = %q, want registered:4 and aud:2", got)
	}
}

// TestWithPrivateRawCopiesTheMap is the copy-on-write test that matters. A
// value receiver copies the map HEADER, not the buckets, so a setter that
// wrote into the receiver's map would edit every existing copy of the claim
// set — including one a caller had already validated and stashed.
func TestWithPrivateRawCopiesTheMap(t *testing.T) {
	t.Parallel()
	base, err := token.NewClaimsValue().WithPrivateRaw("scope", []byte(`"read"`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	derived, err := base.WithPrivateRaw("scope", []byte(`"admin"`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	original, _ := base.PrivateRaw("scope")
	if string(original) != `"read"` {
		t.Fatalf("the original claim set changed to %q — the map was shared", original)
	}
	if updated, _ := derived.PrivateRaw("scope"); string(updated) != `"admin"` {
		t.Fatalf("the derived claim set holds %q, want the new value", updated)
	}
}

// TestAccessorsCopyOut pins that a caller cannot reach back into a claim set
// through a returned slice.
func TestAccessorsCopyOut(t *testing.T) {
	t.Parallel()
	claims, err := token.NewClaimsValue().WithAudience("api").WithPrivateRaw("k", []byte(`1`))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	audience := claims.Audience()
	audience[0] = "tampered"
	if claims.Audience()[0] != "api" {
		t.Fatal("Audience() aliases the claim set")
	}
	raw, _ := claims.PrivateRaw("k")
	raw[0] = '9'
	if again, _ := claims.PrivateRaw("k"); string(again) != "1" {
		t.Fatal("PrivateRaw() aliases the claim set")
	}
}

// TestWithPrivateRawRefusals pins the three refusals: an unnamed claim, a
// claim shadowing a registered name, and a claim set past its cap. The
// shadowing case is the security-relevant one — a second "exp" that the
// temporal validator never reads is a claim an application would trust and
// nothing would enforce.
func TestWithPrivateRawRefusals(t *testing.T) {
	t.Parallel()
	if _, err := token.NewClaimsValue().WithPrivateRaw("", []byte(`1`)); !errs.HasCode(err, token.CodeClaimNameInvalid) {
		t.Fatalf("empty name: got %v, want CLAIM_NAME_INVALID", err)
	}
	for _, reserved := range []string{"iss", "sub", "aud", "exp", "nbf", "iat", "jti"} {
		if _, err := token.NewClaimsValue().WithPrivateRaw(reserved, []byte(`1`)); !errs.HasCode(err, token.CodeClaimNameInvalid) {
			t.Fatalf("reserved name %q: got %v, want CLAIM_NAME_INVALID", reserved, err)
		}
	}
	claims := token.NewClaimsValue()
	for i := range token.MaxPrivateClaims {
		next, err := claims.WithPrivateRaw(fmt.Sprintf("c%d", i), []byte(`1`))
		if err != nil {
			t.Fatalf("claim %d refused early: %v", i, err)
		}
		claims = next
	}
	if _, err := claims.WithPrivateRaw("one-too-many", []byte(`1`)); !errs.HasCode(err, token.CodeTooLarge) {
		t.Fatalf("past the cap: got %v, want TOO_LARGE", err)
	}
	// Replacing an existing claim is not growth, so it stays allowed at the cap.
	if _, err := claims.WithPrivateRaw("c0", []byte(`2`)); err != nil {
		t.Fatalf("replacing at the cap was refused: %v", err)
	}
}

// TestZeroClaimsIsInertAndEmpty pins that the zero value is a valid empty claim
// set rather than a half-built one.
func TestZeroClaimsIsInertAndEmpty(t *testing.T) {
	t.Parallel()
	var zero token.ClaimsValue
	if !zero.IsZero() || !token.NewClaimsValue().IsZero() {
		t.Fatal("the zero ClaimsValue must report IsZero")
	}
	if zero.Audience() != nil || zero.PrivateNames() != nil {
		t.Fatal("the zero ClaimsValue must carry no audience and no private claims")
	}
	if !zero.Expiry().IsZero() {
		t.Fatal("an absent exp must be the zero Time, never the epoch")
	}
}

// TestIsRegisteredClaim pins the closed set the private-claim rule is written
// against.
func TestIsRegisteredClaim(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"iss", "sub", "aud", "exp", "nbf", "iat", "jti"} {
		if !token.IsRegisteredClaim(name) {
			t.Errorf("%q must be a registered claim", name)
		}
	}
	for _, name := range []string{"", "scope", "ISS", "exp ", "role"} {
		if token.IsRegisteredClaim(name) {
			t.Errorf("%q must not be a registered claim", name)
		}
	}
}
