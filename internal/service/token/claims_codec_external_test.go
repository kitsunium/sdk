package token_test

import (
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// notUTF8 is a string that is not valid UTF-8, and notUTF8Twin one that
// differs from it only in the invalid byte. encoding/json decodes both to
// "a�", which is the whole reason an issuer must refuse them.
const (
	notUTF8     = "a\xff"
	notUTF8Twin = "a\xfe"
)

// textFormats returns one issuer per wire format, both lax on everything the
// claim-text tests are not about.
func textFormats(t *testing.T) map[string]coretoken.Issuer {
	t.Helper()
	manual := clock.NewManualClock(epoch)
	jws, err := svctoken.NewHS256Issuer(testSecret(t, 71), svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	_, edPriv := testEdKey(t)
	paseto, err := svctoken.NewPasetoV4Issuer(edPriv, svctoken.PasetoIssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	return map[string]coretoken.Issuer{"JWS": jws, "PASETO": paseto}
}

// withPrivate sets one private claim or fails the test.
func withPrivate(t *testing.T, claims coretoken.ClaimsValue, name, raw string) coretoken.ClaimsValue {
	t.Helper()
	updated, err := claims.WithPrivateRaw(name, []byte(raw))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	return updated
}

// TestIssueRefusesClaimTextThatIsNotUTF8 pins RFC 8725 §3.7 on the ISSUE path,
// where nothing else enforces it.
//
// JSON text is UTF-8, and a byte that is not is either rejected by a strict
// JOSE reader or quietly decoded to U+FFFD — by this package's own verifier
// among others. So an issuer that minted "a\xff" and "a\xfe" as two subjects
// would see them verify as ONE. Every string the issuer writes into a payload
// is covered: the three registered string claims, each audience, and a private
// claim's name (which json.Marshal would rewrite) and bytes (which it would
// pass through). Each case has a valid twin that must still issue, so a
// refusal is proved to be about the byte and not about the claim.
//
// MUTATION (2026-09-11): the checkClaimText call in encodeClaims was deleted.
// Observed nine failures, `Issue = <nil>, want ISSUE_FAILED — a claim that is
// not UTF-8 was minted`, on JWS/{iss,sub,jti,aud,aud_second_of_two} and
// PASETO/{iss,sub,jti,aud}; the four private cases still passed, because
// putPrivate is a separate check. Restored; SHA-256 of claims_codec.go
// identical to the pre-mutation file.
//
// MUTATION (2026-09-11): putPrivate's `!utf8.ValidString(name) ||
// !utf8.Valid(value)` refusal was deleted. Observed the same message on
// exactly the four private cases — {JWS,PASETO}/private_claim_{name,value}.
// Restored; SHA-256 identical.
func TestIssueRefusesClaimTextThatIsNotUTF8(t *testing.T) {
	t.Parallel()
	base := coretoken.NewClaimsValue()
	cases := []struct {
		name         string
		invalid      coretoken.ClaimsValue
		valid        coretoken.ClaimsValue
		multiAudOnly bool
	}{
		{name: "iss", invalid: base.WithIssuer(notUTF8), valid: base.WithIssuer("a")},
		{name: "sub", invalid: base.WithSubject(notUTF8), valid: base.WithSubject("a")},
		{name: "jti", invalid: base.WithID(notUTF8), valid: base.WithID("a")},
		{name: "aud", invalid: base.WithAudience(notUTF8), valid: base.WithAudience("a")},
		{
			name: "aud second of two", multiAudOnly: true,
			invalid: base.WithAudience("api", notUTF8), valid: base.WithAudience("api", "a"),
		},
		{
			name:    "private claim name",
			invalid: withPrivate(t, base, notUTF8, `1`), valid: withPrivate(t, base, "a", `1`),
		},
		{
			name:    "private claim value",
			invalid: withPrivate(t, base, "p", `"`+notUTF8+`"`), valid: withPrivate(t, base, "p", `"a"`),
		},
	}
	for format, issuer := range textFormats(t) {
		for _, tc := range cases {
			//: PASETO carries exactly one audience; its own refusal would
			//: answer this case before the text is ever looked at.
			if tc.multiAudOnly && format == "PASETO" {
				continue
			}
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				if _, err := issuer.Issue(tc.valid); err != nil {
					t.Fatalf("the valid twin was refused: %v", err)
				}
				minted, err := issuer.Issue(tc.invalid)
				if !errs.HasCode(err, coretoken.CodeIssueFailed) || minted != "" {
					t.Fatalf("Issue = %v, want ISSUE_FAILED — a claim that is not UTF-8 was minted", err)
				}
			})
		}
	}
}

// TestMultiByteTextRoundTripsExactly is the other side of the refusal above:
// valid UTF-8 of every width is text, and must come back byte for byte —
// including U+FFFD itself, which is a character and not a symptom.
//
// MUTATION (2026-09-11): the over-eager version of the fix — checkClaimText
// also refusing any non-ASCII string (`|| utf8.RuneCountInString(value) !=
// len(value)`), so invalid bytes are still refused. Observed, and only here:
// `JWS: Issue refused valid UTF-8: [0.2.13.14 ISSUE_FAILED] The token could
// not be issued`; TestIssueRefusesClaimTextThatIsNotUTF8 stayed green.
// Restored; SHA-256 of claims_codec.go identical.
func TestMultiByteTextRoundTripsExactly(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	secret := testSecret(t, 79)
	edPub, edPriv := testEdKey(t)
	const (
		iss = "https://clé.example"
		sub = "ユーザー-42 ☃ 🔑"
		jti = "a� b"
		aud = "api-ü"
	)
	claims := withPrivate(t, coretoken.NewClaimsValue().
		WithIssuer(iss).WithSubject(sub).WithID(jti).WithAudience(aud), "clé", `"valeur-ñ"`)
	jwsIssuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	jwsVerifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{Issuer: iss, Audience: aud, Clock: manual})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	pasetoIssuer, err := svctoken.NewPasetoV4Issuer(edPriv, svctoken.PasetoIssuerConfig{Lifetime: time.Hour, Clock: manual})
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	pasetoVerifier, err := svctoken.NewPasetoV4Verifier(edPub, svctoken.PasetoVerifierConfig{Issuer: iss, Audience: aud, Clock: manual})
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	pairs := map[string]struct {
		issuer   coretoken.Issuer
		verifier coretoken.Verifier
	}{
		"JWS":    {jwsIssuer, jwsVerifier},
		"PASETO": {pasetoIssuer, pasetoVerifier},
	}
	for format, pair := range pairs {
		minted, err := pair.issuer.Issue(claims)
		if err != nil {
			t.Fatalf("%s: Issue refused valid UTF-8: %v", format, err)
		}
		got, err := pair.verifier.Verify(minted)
		if err != nil {
			t.Fatalf("%s: Verify: %v", format, err)
		}
		audience := got.Audience()
		if got.Issuer() != iss || got.Subject() != sub || got.ID() != jti || len(audience) != 1 || audience[0] != aud {
			t.Errorf("%s: registered claims did not survive byte for byte", format)
		}
		if raw, found := got.PrivateRaw("clé"); !found || string(raw) != `"valeur-ñ"` {
			t.Errorf("%s: private claim = %q (found %v), want the exact bytes issued", format, raw, found)
		}
	}
}

// TestIssuerConfigTextIsRefusedAtConstruction pins the half of the rule that is
// decidable before any token exists. "iss", "typ" and "kid" are stamped from
// the configuration into every token an issuer mints, so a value that is not
// UTF-8 would make every mint wrong — and ADR 0031 refuses a configuration
// that cannot be honoured when it is built, not on each use.
//
// MUTATION (2026-09-11): the nonUTF8Knob refusal in validateIssuerConfig was
// deleted. Observed: `JWS Type: got <nil>, want POLICY_MISCONFIGURED`, the
// same for `JWS KeyID` and `JWS Issuer`, and `PASETO Issuer: got <nil>, want
// POLICY_MISCONFIGURED`. Restored; SHA-256 of validate.go identical to
// the pre-mutation file.
func TestIssuerConfigTextIsRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 83)
	_, edPriv := testEdKey(t)
	for name, cfg := range map[string]svctoken.IssuerConfig{
		"JWS Issuer": {Issuer: notUTF8, Lifetime: time.Hour},
		"JWS Type":   {Type: notUTF8, Lifetime: time.Hour},
		"JWS KeyID":  {KeyID: notUTF8Twin, Lifetime: time.Hour},
	} {
		if _, err := svctoken.NewHS256Issuer(secret, cfg); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
			t.Errorf("%s: got %v, want POLICY_MISCONFIGURED", name, err)
		}
	}
	pasetoCfg := svctoken.PasetoIssuerConfig{Issuer: notUTF8, Lifetime: time.Hour}
	if _, err := svctoken.NewPasetoV4Issuer(edPriv, pasetoCfg); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
		t.Errorf("PASETO Issuer: got %v, want POLICY_MISCONFIGURED", err)
	}
	//: multi-byte text is text, in the header as anywhere else.
	valid := svctoken.IssuerConfig{Issuer: "https://clé.example", Type: "at+jwt", KeyID: "clé-1", Lifetime: time.Hour}
	if _, err := svctoken.NewHS256Issuer(secret, valid); err != nil {
		t.Errorf("valid multi-byte configuration: got %v, want acceptance", err)
	}
}
