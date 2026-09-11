package token_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"strconv"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// benchJWKSSizes is the set-size sweep. It brackets MaxKeyCandidatesCeiling
// (16) on both sides — that bound caps candidates under ONE kid, while these
// are distinct kids, so a 64-key set is legal and is what a multi-tenant
// issuer publishes.
var benchJWKSSizes = []int{1, 4, 16, 64}

// benchJWKSKid renders a FIXED-WIDTH kid, for the reason recorded in
// internal/service/crypto/jwk's benchKid: Go compares strings by length
// first, so ragged kids make a lookup's cost depend on how many digits the
// index happens to have rather than on where the match sits.
func benchJWKSKid(index int) string {
	//: two digits cover the whole sweep, so every kid is six octets wide.
	return "key-" + string(rune('0'+index/10)) + string(rune('0'+index%10))
}

// benchJWKSRig is one JWKS verification apparatus: the set verifier and a
// token whose "kid" selects a member of the set it was built from.
type benchJWKSRig struct {
	// verifier is the set verifier under test.
	verifier coretoken.Verifier
	// token is the compact token it authenticates.
	token string
}

// benchECJWK renders a P-256 public key as a JWK carrying kid.
func benchECJWK(b *testing.B, pub *ecdsa.PublicKey, kid string) jwk.KeyValue {
	b.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		b.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	key, kerr := jwk.FromECDSAPublic(der)
	if kerr != nil {
		b.Fatalf("FromECDSAPublic: %v", kerr)
	}
	return key.WithKid(kid)
}

// benchFillerJWK returns a JWK for a freshly generated P-256 key — a member
// that pads the set without ever verifying anything.
func benchFillerJWK(b *testing.B, kid string) jwk.KeyValue {
	b.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	return benchECJWK(b, &priv.PublicKey, kid)
}

// benchJWKSSet builds a size-member set of distinct P-256 keys and puts ring's
// signing key at position, so the caller controls where the match sits.
func benchJWKSSet(b *testing.B, ring benchKeyring, size, position int) (set jwk.Set, kid string) {
	b.Helper()
	keys := make([]jwk.KeyValue, 0, size)
	for index := range size {
		//: exactly one member is the key that actually signed the token.
		if index == position {
			keys = append(keys, benchECJWK(b, &ring.ec.PublicKey, benchJWKSKid(index)))
			continue
		}
		keys = append(keys, benchFillerJWK(b, benchJWKSKid(index)))
	}
	return jwk.NewSet(keys...), benchJWKSKid(position)
}

// benchJWKSRigFor assembles an ES256 rig: a size-member set with the signing
// key at position, and a token stamping that member's kid.
func benchJWKSRigFor(b *testing.B, ring benchKeyring, size, position int) benchJWKSRig {
	b.Helper()
	clk := clock.NewManualClock(benchEpoch)
	set, kid := benchJWKSSet(b, ring, size, position)
	verifier, verr := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{
		Issuer: benchIssuer, Clock: clk,
	})
	if verr != nil {
		b.Fatalf("NewSetVerifier: %v", verr)
	}
	issuer, ierr := svctoken.NewES256Issuer(ring.ec, svctoken.IssuerConfig{
		Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk, KeyID: kid,
	})
	if ierr != nil {
		b.Fatalf("NewES256Issuer: %v", ierr)
	}
	rig := benchJWKSRig{verifier: verifier, token: benchMint(b, issuer, benchClaims(b, "realistic"))}
	benchVerifyAccepts(b, rig.verifier, rig.token)
	return rig
}

// BenchmarkSetVerify prices a JWKS verification against the single-key one
// BenchmarkVerify measures, per algorithm, on the SAME realistic claim set and
// the SAME manual clock — so the difference between the two tables is the key
// SELECTION and BINDING and nothing else.
//
// This is the number the JWKS path had never had: pkg/v1/token's report covers
// only the single-key verifiers, and nothing in the tree benchmarked
// NewSetVerifier at all.
func BenchmarkSetVerify(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, alg := range []string{"HS256", "ES256", "EdDSA"} {
		b.Run(alg, func(b *testing.B) {
			clk := clock.NewManualClock(benchEpoch)
			set := benchSingletonSet(b, ring, alg)
			verifier, verr := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{
				Issuer: benchIssuer, Clock: clk,
			})
			if verr != nil {
				b.Fatalf("NewSetVerifier: %v", verr)
			}
			minted := benchMint(b, benchKeyedIssuer(b, ring, alg, clk), benchClaims(b, "realistic"))
			benchVerifyAccepts(b, verifier, minted)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := verifier.Verify(minted); err != nil {
					b.Fatalf("Verify: %v", err)
				}
			}
		})
	}
}

// benchSingletonSet returns a one-member set holding ring's key for alg.
func benchSingletonSet(b *testing.B, ring benchKeyring, alg string) jwk.Set {
	b.Helper()
	var (
		key jwk.KeyValue
		err error
	)
	switch alg {
	//: the symmetric member — an oct JWK carrying the shared secret.
	case "HS256":
		key, err = jwk.FromSecret(ring.secret)
	//: the Edwards member.
	case "EdDSA":
		key, err = jwk.FromEd25519Public(ring.edPub)
	//: the P-256 member.
	default:
		key = benchECJWK(b, &ring.ec.PublicKey, benchJWKSKid(0))
	}
	if err != nil {
		b.Fatalf("JWK for %s: %v", alg, err)
	}
	return jwk.NewSet(key.WithKid(benchJWKSKid(0)))
}

// benchKeyedIssuer returns an issuer for alg that stamps the set's kid.
func benchKeyedIssuer(b *testing.B, ring benchKeyring, alg string, clk clock.Clock) coretoken.Issuer {
	b.Helper()
	cfg := svctoken.IssuerConfig{
		Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk, KeyID: benchJWKSKid(0),
	}
	var (
		issuer coretoken.Issuer
		err    error
	)
	switch alg {
	//: the symmetric binding.
	case "HS256":
		issuer, err = svctoken.NewHS256Issuer(ring.secret, cfg)
	//: JOSE EdDSA.
	case "EdDSA":
		issuer, err = svctoken.NewEdDSAIssuer(ring.edPriv, cfg)
	//: ECDSA P-256.
	default:
		issuer, err = svctoken.NewES256Issuer(ring.ec, cfg)
	}
	if err != nil {
		b.Fatalf("issuer %s: %v", alg, err)
	}
	return issuer
}

// BenchmarkSetVerifySize sweeps the set size with the match FIRST and LAST, so
// the selection scan's slope is a number rather than an adjective.
//
// first and last are a CONTROL, not two points of a slope: the lookup has no
// early exit, so the two must agree at every size. A divergence is a defect in
// the harness or in the scan, and one draft of this pair had exactly that —
// see the kid-width note on benchJWKSKid.
func BenchmarkSetVerifySize(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, size := range benchJWKSSizes {
		positions := map[string]int{"first": 0, "last": size - 1}
		for name, position := range positions {
			b.Run("n="+strconv.Itoa(size)+"/match="+name, func(b *testing.B) {
				rig := benchJWKSRigFor(b, ring, size, position)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := rig.verifier.Verify(rig.token); err != nil {
						b.Fatalf("Verify: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkSetVerifyRotation prices the rotation window: two keys under ONE
// kid, the token signed by the SECOND. It is the worst LEGITIMATE case — the
// first candidate binds, gates and runs a full signature verification before
// being rejected — and it is what RFC 7517 §4.5 leaves open by only SHOULD-ing
// distinct kids.
func BenchmarkSetVerifyRotation(b *testing.B) {
	ring := newBenchKeyring(b)
	clk := clock.NewManualClock(benchEpoch)
	kid := benchJWKSKid(0)
	set := jwk.NewSet(benchFillerJWK(b, kid), benchECJWK(b, &ring.ec.PublicKey, kid))
	verifier, verr := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{
		Issuer: benchIssuer, Clock: clk,
	})
	if verr != nil {
		b.Fatalf("NewSetVerifier: %v", verr)
	}
	minted := benchMint(b, benchKeyedIssuer(b, ring, "ES256", clk), benchClaims(b, "realistic"))
	benchVerifyAccepts(b, verifier, minted)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := verifier.Verify(minted); err != nil {
			b.Fatalf("Verify: %v", err)
		}
	}
}

// BenchmarkSetVerifyRefusal prices the two selection refusals, which is the
// half of the report a denial-of-service reading needs.
//
// no_kid is the row a reconnaissance of this path predicted would "force every
// candidate to be tried". It does not: candidates refuses an empty kid with
// KeyIDMissing before the set is touched at all, so the row measures a parse
// and a string comparison. The prediction is refuted by the number.
func BenchmarkSetVerifyRefusal(b *testing.B) {
	ring := newBenchKeyring(b)
	clk := clock.NewManualClock(benchEpoch)
	set, _ := benchJWKSSet(b, ring, 64, 0)
	verifier, verr := svctoken.NewSetVerifier(set, svctoken.VerifierConfig{
		Issuer: benchIssuer, Clock: clk,
	})
	if verr != nil {
		b.Fatalf("NewSetVerifier: %v", verr)
	}
	tokens := map[string]string{
		"no_kid":      benchMint(b, benchUnkeyedES256Issuer(b, ring, clk), benchClaims(b, "realistic")),
		"unknown_kid": benchMint(b, benchStrayIssuer(b, ring, clk), benchClaims(b, "realistic")),
	}
	for name, minted := range tokens {
		b.Run(name, func(b *testing.B) {
			//: the row is a REFUSAL; an acceptance here would measure the wrong path.
			if _, err := verifier.Verify(minted); err == nil {
				b.Fatalf("%s: verified, want a refusal", name)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, sinkVerifyErr = verifier.Verify(minted)
			}
		})
	}
}

// sinkVerifyErr receives a refusal so the compiler cannot elide the call.
var sinkVerifyErr error

// benchUnkeyedES256Issuer returns an ES256 issuer stamping no "kid" at all.
func benchUnkeyedES256Issuer(b *testing.B, ring benchKeyring, clk clock.Clock) coretoken.Issuer {
	b.Helper()
	issuer, err := svctoken.NewES256Issuer(ring.ec, svctoken.IssuerConfig{
		Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk,
	})
	if err != nil {
		b.Fatalf("NewES256Issuer: %v", err)
	}
	return issuer
}

// benchStrayIssuer returns an ES256 issuer stamping a kid the set never holds.
func benchStrayIssuer(b *testing.B, ring benchKeyring, clk clock.Clock) coretoken.Issuer {
	b.Helper()
	issuer, err := svctoken.NewES256Issuer(ring.ec, svctoken.IssuerConfig{
		Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk, KeyID: benchJWKSKid(99),
	})
	if err != nil {
		b.Fatalf("NewES256Issuer: %v", err)
	}
	return issuer
}

// BenchmarkSetVerifierConstruction prices building the verifier, which a
// service pays once per JWKS refresh. It is published beside the per-request
// rows because moving work out of the request and into construction is only
// defensible when the construction cost is known.
func BenchmarkSetVerifierConstruction(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, size := range benchJWKSSizes {
		set, _ := benchJWKSSet(b, ring, size, 0)
		b.Run("n="+strconv.Itoa(size), makeConstructionBench(set))
	}
}

// makeConstructionBench returns the timed body for one set size.
//
// The set is a PARAMETER rather than a closed-over loop-body local — the shape
// pkg/v1/codec's makeMarshalBench uses — so the harness's own value does not
// escape to the heap for the duration of a window that is measuring
// allocations.
func makeConstructionBench(set jwk.Set) func(b *testing.B) {
	return func(b *testing.B) {
		cfg := svctoken.VerifierConfig{Issuer: benchIssuer, Clock: clock.NewManualClock(benchEpoch)}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkVerifier, sinkVerifyErr = svctoken.NewSetVerifier(set, cfg)
		}
	}
}

// sinkVerifier receives a constructed verifier.
var sinkVerifier coretoken.Verifier
