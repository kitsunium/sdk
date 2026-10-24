//go:build !race

// Package token — the allocation contract of VerifierInto.VerifyInto.
//
// VerifyInto exists to verify an HS256 token on an event loop without
// allocating: the header, the signature and the claims are decoded into stack
// arrays, the MAC sums into the caller's scratch, and every claim is copied
// into the caller's ClaimsBuf. ADR 0186 §D9 step 4 makes that a contract, and
// this file is its gate.
//
// It is hand-written rather than a design budget: a budget names a designed
// symbol, the port's method is designed in core, and a core fixture may not
// import the service layer the only implementation lives in, while the
// facade's alias of the port is not a designed symbol of its own (kit
// v0.1.0-rc.15). The window is the package's own (allocwindow_test.go).
//
// Race off, for the reason split_alloc_internal_test.go gives: the race
// detector allocates on its own. The race-off allocation lane runs this
// package (tools/alloc-lane-targets.txt, SDK-wide rule 12).
//
// MUTATION (2026-10-24). With readJOSEHeader writing its view through a
// pointer — the shape it had first — escape analysis moved the 512-octet
// header buffer to the heap: one allocation a call, which the first draft of
// this gate counted (30 000 in 30 000 calls) before the view was returned by
// value.
package token

import (
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// verifyIntoWarmup is the warm-up kit's generated budgets make (kitPerfWarmup).
const verifyIntoWarmup int = 30000

// sinkVerifyInto keeps each verdict alive.
var sinkVerifyInto error

// TestVerifyIntoAllocatesNothing verifies one realistic accepted token — an
// issuer, a subject, an audience array, the three instants, an id and three
// private claims, two of them declared — through one scratch and one buffer,
// judged against an issuer and an audience: zero allocations.
func TestVerifyIntoAllocatesNothing(t *testing.T) {
	key, err := corecrypto.NewKey(make([]byte, corecrypto.KeyLen))
	//: a zero key is still a key of the right length.
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	issuer, err := NewHS256Issuer(key, IssuerConfig{Issuer: "auth", Lifetime: time.Hour, Clock: clk})
	//: the issuer of the token under test.
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	claims := coretoken.NewClaimsValue().WithSubject("4242").WithAudience("web", "api").WithID("j-1")
	//: three private claims, one of them not a string.
	for name, raw := range map[string]string{"username": `"ada"`, "role": `"admin"`, "scopes": `["a","b"]`} {
		claims, err = claims.WithPrivateRaw(name, []byte(raw))
		//: the claim set is valid by construction.
		if err != nil {
			t.Fatalf("WithPrivateRaw: %v", err)
		}
	}
	minted, err := issuer.Issue(claims)
	//: the token every call verifies.
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := NewHS256Verifier(key, VerifierConfig{Issuer: "auth", Audience: "api", Clock: clk})
	//: the verifier under test.
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	into := verifier.(coretoken.VerifierInto)
	scratch := into.NewScratch()
	buf, err := coretoken.NewClaimsBuf("username", "scopes")
	//: two declared claims, one of them wrong-typed.
	if err != nil {
		t.Fatalf("NewClaimsBuf: %v", err)
	}
	tok := []byte(minted)
	//: an error here would mean the window measures a refusal.
	if verr := into.VerifyInto(scratch, tok, buf); verr != nil {
		t.Fatalf("VerifyInto refused the token: %v", verr)
	}
	//: the runtime builds a type assertion's cache lazily, on about one miss
	//: in 1024 — crypto/internal/fips140/hmac's Sum asserts its hashes — so
	//: as many warm-up calls as kit's budgets make are paid outside the window.
	for range verifyIntoWarmup {
		sinkVerifyInto = into.VerifyInto(scratch, tok, buf)
	}
	got := mallocsOver(allocRuns, func() {
		sinkVerifyInto = into.VerifyInto(scratch, tok, buf)
	})
	reportWindow(t, "VerifyInto of an accepted token", got)
	//: zero, not "small": that is the port's reason to exist.
	if got.own != 0 {
		t.Fatalf("VerifyInto performed %d allocations in %d calls, want 0", got.own, allocRuns)
	}
}
