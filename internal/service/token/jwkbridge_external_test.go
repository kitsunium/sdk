package token_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// ecJWK renders an ECDSA public key as a JWK carrying kid.
func ecJWK(t *testing.T, pub *ecdsa.PublicKey, kid string) jwk.KeyValue {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	key, err := jwk.FromECDSAPublic(der)
	if err != nil {
		t.Fatalf("FromECDSAPublic: %v", err)
	}
	return key.WithKid(kid)
}

// TestVerifierFromJWKBindsTheKeysAlgorithm pins the mapping and, more
// importantly, WHERE it reads from: the key's own kty/crv, which the relying
// party fetched from a publisher it trusts — never the token's alg header.
func TestVerifierFromJWKBindsTheKeysAlgorithm(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	ec := testECKey(t)
	edPub, edPriv := testEdKey(t)
	secret := testSecret(t, 53)
	issuerCfg := svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual}
	verifierCfg := svctoken.VerifierConfig{Clock: manual}

	edKey, err := jwk.FromEd25519Public(edPub)
	if err != nil {
		t.Fatalf("FromEd25519Public: %v", err)
	}
	octKey, err := jwk.FromSecret(secret)
	if err != nil {
		t.Fatalf("FromSecret: %v", err)
	}
	cases := map[string]struct {
		key   jwk.KeyValue
		issue func() (coretoken.Issuer, error)
	}{
		"EC P-256 binds ES256": {
			ecJWK(t, &ec.PublicKey, "ec-1"),
			func() (coretoken.Issuer, error) { return svctoken.NewES256Issuer(ec, issuerCfg) },
		},
		"OKP Ed25519 binds EdDSA": {
			edKey,
			func() (coretoken.Issuer, error) { return svctoken.NewEdDSAIssuer(edPriv, issuerCfg) },
		},
		"oct binds HS256": {
			octKey,
			func() (coretoken.Issuer, error) { return svctoken.NewHS256Issuer(secret, issuerCfg) },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			issuer, err := tc.issue()
			if err != nil {
				t.Fatalf("issuer: %v", err)
			}
			minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			verifier, err := svctoken.NewVerifierFromJWK(tc.key, verifierCfg)
			if err != nil {
				t.Fatalf("NewVerifierFromJWK: %v", err)
			}
			if _, verr := verifier.Verify(minted); verr != nil {
				t.Fatalf("Verify: %v", verr)
			}
		})
	}
}

// TestJWKConfusionIsRefused is the JWK-shaped version of the confusion attack:
// the attacker has the published EC JWK and mints an HS256 token whose MAC
// secret is derived from it. The binding is built from the KEY, reports ES256,
// and the header comparison refuses.
func TestJWKConfusionIsRefused(t *testing.T) {
	t.Parallel()
	ec := testECKey(t)
	published := ecJWK(t, &ec.PublicKey, "ec-1")
	//: whatever bytes the attacker chooses, the verifier never reaches a MAC.
	forged := forge(`{"alg":"HS256","kid":"ec-1","typ":"JWT"}`, `{"sub":"admin"}`,
		func(string) []byte { return make([]byte, 32) })
	single, err := svctoken.NewVerifierFromJWK(published, laxConfig())
	if err != nil {
		t.Fatalf("NewVerifierFromJWK: %v", err)
	}
	if _, verr := single.Verify(forged); !errs.HasCode(verr, coretoken.CodeAlgorithmMismatch) {
		t.Fatalf("single-key JWK verifier: got %v, want ALGORITHM_MISMATCH", verr)
	}
	set, err := svctoken.NewSetVerifier(jwk.NewSet(published), laxConfig())
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	//: the set verifier skips a candidate whose algorithm does not match and
	//: runs out of candidates, so the answer is the generic refusal — never a
	//: MAC computed with the public key.
	if _, verr := set.Verify(forged); !errs.HasCode(verr, coretoken.CodeSignatureInvalid) {
		t.Fatalf("set verifier: got %v, want SIGNATURE_INVALID", verr)
	}
}

// TestSetVerifierResolvesAmbiguousKidBySignature is the explicit answer to
// jwk.Set's refusal to choose. ByKid cannot pick between two keys under one
// kid because nothing in the document distinguishes them; a signature does, so
// this verifier tries the candidates and lets the cryptography decide.
func TestSetVerifierResolvesAmbiguousKidBySignature(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	retiring, current := testECKey(t), testECKey(t)
	//: a rotation window: both keys published under the same kid.
	set := jwk.NewSet(
		ecJWK(t, &retiring.PublicKey, "rotating"),
		ecJWK(t, &current.PublicKey, "rotating"),
	)
	//: jwk.Set itself refuses to choose — that refusal is the premise here.
	if _, err := set.ByKid("rotating"); !errs.HasCode(err, jwk.CodeJWKAmbiguousKid) {
		t.Fatalf("jwk.Set.ByKid should refuse a duplicate kid, got %v", err)
	}
	verifier, err := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{Clock: manual})
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	//: the SECOND candidate is the one that signed, so document order alone
	//: would have produced the wrong answer.
	issuer, err := svctoken.NewES256Issuer(current, svctoken.IssuerConfig{
		Lifetime: time.Hour, Clock: manual, KeyID: "rotating",
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u").WithID("t-1"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, verr := verifier.Verify(minted)
	if verr != nil {
		t.Fatalf("Verify: %v", verr)
	}
	if claims.ID() != "t-1" {
		t.Fatalf("claims = %v, want the token's own jti", claims)
	}
	//: and the retiring key still verifies its own tokens under the same kid.
	retiringIssuer, err := svctoken.NewES256Issuer(retiring, svctoken.IssuerConfig{
		Lifetime: time.Hour, Clock: manual, KeyID: "rotating",
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	older, err := retiringIssuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, verr := verifier.Verify(older); verr != nil {
		t.Fatalf("the retiring key's token must still verify: %v", verr)
	}
}

// TestSetVerifierBoundsTheCandidateCount pins that trying candidates is
// bounded work. Each attempt is a signature verification, and the candidate
// count is chosen by whoever published the key set — so an unbounded loop
// would let the publisher price every request.
func TestSetVerifierBoundsTheCandidateCount(t *testing.T) {
	t.Parallel()
	keys := make([]jwk.KeyValue, 0, 6)
	for range 6 {
		keys = append(keys, ecJWK(t, &testECKey(t).PublicKey, "crowded"))
	}
	verifier, err := svctoken.NewSetVerifier(jwk.NewSet(keys...),
		svctoken.VerifierConfig{MaxKeyCandidates: 2, AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	forged := forge(`{"alg":"ES256","kid":"crowded","typ":"JWT"}`, `{"sub":"u"}`,
		func(string) []byte { return make([]byte, 64) })
	if _, verr := verifier.Verify(forged); !errs.HasCode(verr, svctoken.CodeKeyIDAmbiguous) {
		t.Fatalf("six candidates at a bound of two: got %v, want KEY_ID_AMBIGUOUS", verr)
	}
}

// TestSetVerifierRequiresAKid pins that a set verifier selects rather than
// searches: a token with no kid is refused instead of tried against every key
// the set holds.
func TestSetVerifierRequiresAKid(t *testing.T) {
	t.Parallel()
	ec := testECKey(t)
	verifier, err := svctoken.NewSetVerifier(jwk.NewSet(ecJWK(t, &ec.PublicKey, "ec-1")), laxConfig())
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	issuer, err := svctoken.NewES256Issuer(ec, svctoken.IssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	//: a perfectly valid token — it simply carries no kid.
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, verr := verifier.Verify(minted); !errs.HasCode(verr, svctoken.CodeKeyIDMissing) {
		t.Fatalf("no kid: got %v, want KEY_ID_MISSING", verr)
	}
	strayIssuer, err := svctoken.NewES256Issuer(ec, svctoken.IssuerConfig{
		AllowMissingExpiry: true, KeyID: "some-other-key",
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	//: a token this very key signed, routed to a kid the set does not hold.
	stray, err := strayIssuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, verr := verifier.Verify(stray); !errs.HasCode(verr, svctoken.CodeKeyNotFound) {
		t.Fatalf("unknown kid: got %v, want KEY_NOT_FOUND", verr)
	}
}

// TestJWKAdvisoryMembersAreHonoured pins that a publisher's own statements
// about their key are enforced. Ignoring them is choosing to know less than
// you were told.
func TestJWKAdvisoryMembersAreHonoured(t *testing.T) {
	t.Parallel()
	base := ecJWK(t, &testECKey(t).PublicKey, "ec-1")
	for name, key := range map[string]jwk.KeyValue{
		"use is not sig":          base.WithUse("enc"),
		"key_ops excludes verify": base.WithKeyOps("sign"),
		"alg contradicts kty/crv": base.WithAlg("HS256"),
	} {
		if _, err := svctoken.NewVerifierFromJWK(key, laxConfig()); !errs.HasCode(err, coretoken.CodeKeyUnsuitable) {
			t.Errorf("%s: got %v, want KEY_UNSUITABLE", name, err)
		}
	}
	for name, key := range map[string]jwk.KeyValue{
		"use is sig":              base.WithUse("sig"),
		"key_ops includes verify": base.WithKeyOps("verify", "sign"),
		"alg agrees":              base.WithAlg("ES256"),
	} {
		if _, err := svctoken.NewVerifierFromJWK(key, laxConfig()); err != nil {
			t.Errorf("%s: got %v, want acceptance", name, err)
		}
	}
}

// TestUnsupportedJWKsAreRefused pins that a key the SDK cannot verify with is
// refused rather than half-accepted. P-384 is representable as a JWK (an
// identity provider routinely publishes one) and is not signable here.
func TestUnsupportedJWKsAreRefused(t *testing.T) {
	t.Parallel()
	if _, err := svctoken.NewSetVerifier(jwk.Set{}, laxConfig()); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
		t.Fatalf("empty set: got %v, want POLICY_MISCONFIGURED", err)
	}
	if _, err := svctoken.NewVerifierFromJWK(jwk.KeyValue{}, laxConfig()); !errs.HasCode(err, coretoken.CodeKeyUnsuitable) {
		t.Fatalf("zero JWK: got %v, want KEY_UNSUITABLE", err)
	}
}

// TestUnverifiableCandidatesStillCountAgainstTheBound pins the property the
// 2026-09 pre-binding refactor could most easily have destroyed, and destroyed
// invisibly: MaxKeyCandidates bounds the keys PUBLISHED under one kid, not the
// subset this SDK happens to be able to verify with.
//
// NewSetVerifier now derives every member's binding once at construction, and
// the obvious shape for that is to keep only the members that bound. That is a
// silent widening of a denial-of-service bound: a publisher who lists six keys
// under one kid, three of them on curves the SDK refuses, would go from
// KEY_ID_AMBIGUOUS to three full signature verifications per request — and
// every functional test in this file would still pass, because the surviving
// keys behave identically. indexByKid therefore records a refused member as an
// UNUSABLE entry rather than dropping it.
//
// MUTATION (2026-09-11): indexByKid's append was made conditional —
// `if berr != nil { continue }`. Observed: `four published candidates at a
// bound of three: got <nil>, want KEY_ID_AMBIGUOUS` — the token VERIFIED,
// because two of the four were dropped before the bound was applied. Restored;
// the file's SHA-256 is byte-identical to the pre-mutation one.
func TestUnverifiableCandidatesStillCountAgainstTheBound(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	signing := testECKey(t)
	//: two P-384 keys the jwk package represents happily and this package
	//: refuses: kty=EC with a crv the SDK signs nothing on.
	set := jwk.NewSet(
		p384JWK(t, "crowded"), p384JWK(t, "crowded"),
		ecJWK(t, &testECKey(t).PublicKey, "crowded"),
		ecJWK(t, &signing.PublicKey, "crowded"),
	)
	verifier, err := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{
		MaxKeyCandidates: 3, Clock: manual,
	})
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	issuer, err := svctoken.NewES256Issuer(signing, svctoken.IssuerConfig{
		Lifetime: time.Hour, Clock: manual, KeyID: "crowded",
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	//: a genuinely valid token, signed by a member of the set — so the only
	//: thing that can refuse it is the candidate bound.
	minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, verr := verifier.Verify(minted); !errs.HasCode(verr, svctoken.CodeKeyIDAmbiguous) {
		t.Fatalf("four published candidates at a bound of three: got %v, want KEY_ID_AMBIGUOUS", verr)
	}
}

// p384JWK renders a fresh P-384 public key as a JWK carrying kid. It is
// representable by the jwk package and refused by bindJWK, which is exactly
// the member shape the bound has to keep counting.
func p384JWK(t *testing.T, kid string) jwk.KeyValue {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P384(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return ecJWK(t, &priv.PublicKey, kid)
}
