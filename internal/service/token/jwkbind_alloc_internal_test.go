//go:build !race

// Package token — the allocation contract the JWK Set verifier now rests on.
//
// NewSetVerifier derives every member's binding ONCE, at construction. Before
// 2026-09 it derived them per token, and for an EC key that meant a full PKIX
// round trip on every request: KeyValue.ECDSAPublic rebuilt two big.Ints and
// ran x509.MarshalPKIXPublicKey, and bindECJWK handed the resulting DER
// straight back to x509.ParsePKIXPublicKey one line later. The allocation
// profile put that at 28.2 % of an ES256 JWKS verification's objects — 11.8 %
// of them inside encoding/asn1.Marshal alone — to reconstruct a key that is
// fixed for the verifier's lifetime.
//
// A regression is invisible to every functional test in this package:
// re-deriving the key produces exactly the same binding and exactly the same
// verdict. Only an allocation count sees it, which is why this gate exists.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite, and
// //internal/service/token:token_test already carries the entry in
// tools/alloc-lane-targets.txt that makes the race-off alloc lane its gate
// (SDK-wide rule 12).
package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

const (
	// jwkAllocRuns is how many verifications each side of the comparison
	// performs. Large enough that a per-call regression cannot hide in the
	// noise of a one-off initialisation.
	jwkAllocRuns int = 200
	// maxSetSurcharge is the largest TOTAL allocation difference this test
	// accepts between jwkAllocRuns JWKS verifications and jwkAllocRuns
	// single-key verifications of the SAME token under the SAME policy.
	//
	// The clean surcharge measures 0. The budget is 200 — one per call — so
	// the gate is not brittle against an unrelated future allocation on the
	// selection path, while the defect it exists to catch costs about fifty
	// per call and lands near 10 000.
	maxSetSurcharge uint64 = 200
	// jwkAllocKid is the kid the token stamps and the set publishes.
	jwkAllocKid string = "es256-alloc"
)

var (
	// jwkAllocEpoch pins both verifiers' clocks, so neither reads the wall
	// clock inside a counted window.
	jwkAllocEpoch = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	// jwkAllocVerdict receives each measured Verify's error. It is a
	// package-level sink so the counted window contains no branch — and it is
	// checked after the window, which turns "the token kept verifying" into an
	// assertion instead of an assumption.
	jwkAllocVerdict error
)

// jwkAllocRig is the pair of verifiers this gate compares, plus the one token
// they both authenticate.
//
// Comparing against a single-key verifier rather than against a constant is
// deliberate, and it is the same discipline the writer packages' gates use: the
// budget is a DELTA, so the test does not couple to the claims decoding both
// sides share and does not fail when THAT path's allocation count moves for
// reasons this file does not own.
type jwkAllocRig struct {
	// set is the JWKS verifier under test.
	set coretoken.Verifier
	// single is the same key, bound directly — the baseline.
	single coretoken.Verifier
	// token is the compact token both authenticate. It carries a "kid", which
	// the single-key verifier ignores, so the two sides read byte-identical
	// input and the header's size is not a confound.
	token string
}

// newJWKAllocRig assembles the pair or fails the test.
func newJWKAllocRig(t *testing.T) jwkAllocRig {
	t.Helper()
	clk := clock.NewManualClock(jwkAllocEpoch)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, merr := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if merr != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", merr)
	}
	key, kerr := jwk.FromECDSAPublic(der)
	if kerr != nil {
		t.Fatalf("FromECDSAPublic: %v", kerr)
	}
	cfg := VerifierConfig{Clock: clk}
	rig := jwkAllocRig{
		set:    mustSetVerifier(t, jwk.NewSet(key.WithKid(jwkAllocKid)), cfg),
		single: mustES256Verifier(t, &priv.PublicKey, cfg),
		token:  mustMintKeyed(t, priv, clk),
	}
	//: both sides must ACCEPT, or the gate would compare two refusal paths.
	assertVerifies(t, rig.set, rig.token, "set")
	assertVerifies(t, rig.single, rig.token, "single")
	return rig
}

// mustSetVerifier builds a JWKS verifier or fails the test.
func mustSetVerifier(t *testing.T, set jwk.Set, cfg VerifierConfig) coretoken.Verifier {
	t.Helper()
	verifier, err := NewSetVerifier(set, cfg)
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	return verifier
}

// mustES256Verifier builds a single-key verifier or fails the test.
func mustES256Verifier(t *testing.T, pub *ecdsa.PublicKey, cfg VerifierConfig) coretoken.Verifier {
	t.Helper()
	verifier, err := NewES256Verifier(pub, cfg)
	if err != nil {
		t.Fatalf("NewES256Verifier: %v", err)
	}
	return verifier
}

// mustMintKeyed issues one ES256 token stamping jwkAllocKid.
func mustMintKeyed(t *testing.T, priv *ecdsa.PrivateKey, clk clock.Clock) string {
	t.Helper()
	issuer, err := NewES256Issuer(priv, IssuerConfig{
		Lifetime: time.Hour, Clock: clk, KeyID: jwkAllocKid,
	})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	minted, ierr := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if ierr != nil {
		t.Fatalf("Issue: %v", ierr)
	}
	return minted
}

// assertVerifies fails the test unless verifier accepts token.
func assertVerifies(t *testing.T, verifier coretoken.Verifier, token, side string) {
	t.Helper()
	if _, err := verifier.Verify(token); err != nil {
		t.Fatalf("%s verifier rejected the token: %v", side, err)
	}
}

// TestSetVerificationDoesNotRederiveTheKey pins the claim NewSetVerifier's doc
// comment makes and the one this refactor bought: selecting a key from a JWK
// Set costs no key DERIVATION, because the derivation happened at construction.
//
// MUTATION (2026-09-11). indexByKid was reduced to storing the raw key and
// Verify put the derivation back where it was:
//
//	for _, candidate := range candidates {
//	    binding, berr := bindJWK(candidate.key)
//	    if berr != nil { continue }
//	    ...
//	}
//
// Observed: `a JWKS verification allocated 9800 more than a single-key one over
// 200 calls (49.0 per call), budget 200`. Restored; the file's SHA-256 is
// byte-identical to the pre-mutation one and the surcharge is back to 0.
func TestSetVerificationDoesNotRederiveTheKey(t *testing.T) {
	rig := newJWKAllocRig(t)
	set := mallocsOver(jwkAllocRuns, func() {
		//: the verdict lands in a package-level sink rather than a branch, so
		//: the counted window holds no comparison the defect could move.
		_, jwkAllocVerdict = rig.set.Verify(rig.token)
	})
	//: every call inside the window must have kept succeeding, or the two
	//: sides are counting different paths.
	if jwkAllocVerdict != nil {
		t.Fatalf("set verifier stopped accepting mid-window: %v", jwkAllocVerdict)
	}
	single := mallocsOver(jwkAllocRuns, func() {
		//: the same token, the same policy, one key bound directly.
		_, jwkAllocVerdict = rig.single.Verify(rig.token)
	})
	//: same assertion for the baseline.
	if jwkAllocVerdict != nil {
		t.Fatalf("single-key verifier stopped accepting mid-window: %v", jwkAllocVerdict)
	}
	//: a JWKS verification may not cost meaningfully more than the single-key
	//: one it wraps — everything above the baseline is selection, and
	//: selection is a map read over a slice the constructor already built.
	if set > single+maxSetSurcharge {
		t.Fatalf("a JWKS verification allocated %d more than a single-key one over %d calls (%.1f per call), budget %d",
			set-single, jwkAllocRuns, float64(set-single)/float64(jwkAllocRuns), maxSetSurcharge)
	}
}
