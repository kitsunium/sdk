package token_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// benchIssuer is the issuer string every benchmark token carries, so the
// verifier's issuer check is exercised rather than skipped.
const benchIssuer string = "https://auth.example"

// benchEpoch is the instant every benchmark's manual clock is pinned to.
//
// Every timing row below uses a clock.ManualClock rather than the wall clock,
// for two reasons that both matter to the numbers: an expired-token row cannot
// exist without moving time by assignment, and a manual Now() is a struct read
// where clock.System.Now() is a vDSO call. BenchmarkVerifyClockSource prices
// that difference explicitly so a reader can add it back.
var benchEpoch = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

// benchAlgorithms is the closed set of algorithm names every per-algorithm
// benchmark iterates. A caller choosing one needs the cost beside the choice,
// so no algorithm this package ships is allowed to be missing from a row.
var benchAlgorithms = []string{"HS256", "ES256", "EdDSA", "PasetoV4Public"}

// benchKeyring holds one keypair per algorithm, generated once for the whole
// binary so key generation never lands inside a timed loop.
type benchKeyring struct {
	// secret is the HS256 shared secret.
	secret corecrypto.Key
	// ec is the ES256 P-256 keypair.
	ec *ecdsa.PrivateKey
	// edPub / edPriv back both JOSE EdDSA and PASETO v4.public.
	edPub  ed25519.PublicKey
	edPriv ed25519.PrivateKey
}

// newBenchKeyring generates the three keypairs or fails the benchmark.
func newBenchKeyring(b *testing.B) benchKeyring {
	b.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	//: a fixed, non-degenerate secret — the value never affects HMAC's cost.
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	secret, err := corecrypto.NewKey(raw)
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	return benchKeyring{secret: secret, ec: ec, edPub: edPub, edPriv: edPriv}
}

// issuerFor returns the issuer for one algorithm name under clk.
func (k benchKeyring) issuerFor(b *testing.B, alg string, clk clock.Clock) coretoken.Issuer {
	b.Helper()
	cfg := svctoken.IssuerConfig{Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk}
	var (
		issuer coretoken.Issuer
		err    error
	)
	switch alg {
	//: the symmetric binding.
	case "HS256":
		issuer, err = svctoken.NewHS256Issuer(k.secret, cfg)
	//: ECDSA P-256 with the fixed-width R||S encoding.
	case "ES256":
		issuer, err = svctoken.NewES256Issuer(k.ec, cfg)
	//: JOSE EdDSA.
	case "EdDSA":
		issuer, err = svctoken.NewEdDSAIssuer(k.edPriv, cfg)
	//: PASETO v4.public, whose config is flat rather than embedded.
	default:
		issuer, err = svctoken.NewPasetoV4Issuer(k.edPriv, svctoken.PasetoIssuerConfig{
			Issuer: benchIssuer, Lifetime: time.Hour, Clock: clk,
		})
	}
	if err != nil {
		b.Fatalf("issuer %s: %v", alg, err)
	}
	return issuer
}

// verifierFor returns the verifier for one algorithm name under clk.
func (k benchKeyring) verifierFor(b *testing.B, alg string, clk clock.Clock) coretoken.Verifier {
	b.Helper()
	cfg := svctoken.VerifierConfig{Issuer: benchIssuer, Clock: clk}
	var (
		verifier coretoken.Verifier
		err      error
	)
	switch alg {
	//: the symmetric binding.
	case "HS256":
		verifier, err = svctoken.NewHS256Verifier(k.secret, cfg)
	//: ECDSA P-256.
	case "ES256":
		verifier, err = svctoken.NewES256Verifier(&k.ec.PublicKey, cfg)
	//: JOSE EdDSA.
	case "EdDSA":
		verifier, err = svctoken.NewEdDSAVerifier(k.edPub, cfg)
	//: PASETO v4.public.
	default:
		verifier, err = svctoken.NewPasetoV4Verifier(k.edPub, svctoken.PasetoVerifierConfig{
			Issuer: benchIssuer, Clock: clk,
		})
	}
	if err != nil {
		b.Fatalf("verifier %s: %v", alg, err)
	}
	return verifier
}

// benchClaims returns the named claim set.
//
// "minimal" is what an issuer stamps when the caller supplies nothing: iss,
// iat and exp. "realistic" adds sub, aud and jti plus three private claims of
// the shape a real deployment carries — a scope string, a role array and a
// tenant identifier. The pair exists so a reader can price their own payload
// rather than interpolate from one number.
func benchClaims(b *testing.B, shape string) coretoken.ClaimsValue {
	b.Helper()
	//: the issuer stamps iss/iat/exp itself; the caller adds nothing.
	if shape == "minimal" {
		return coretoken.NewClaimsValue()
	}
	claims := coretoken.NewClaimsValue().
		WithSubject("user-8f2c41d0-6b3e-4a19-9c77-0e5d1a2b3c4d").
		WithAudience("https://api.example").
		WithID("01JQ8Z4X7K2M9P3R5T6V8W0Y1A")
	for name, raw := range map[string]string{
		"scope":  `"openid profile email offline_access"`,
		"roles":  `["admin","billing:read","reports:write"]`,
		"tenant": `"acme-corp-eu-west-1"`,
	} {
		next, err := claims.WithPrivateRaw(name, json.RawMessage(raw))
		if err != nil {
			b.Fatalf("WithPrivateRaw(%s): %v", name, err)
		}
		claims = next
	}
	return claims
}

// BenchmarkIssue prices minting one token, per algorithm and per claim-set
// size. It is the half of the domain that runs once per login; Verify below is
// the half that runs on every authenticated request.
func BenchmarkIssue(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, alg := range benchAlgorithms {
		for _, shape := range []string{"minimal", "realistic"} {
			b.Run(alg+"/"+shape, func(b *testing.B) {
				issuer := ring.issuerFor(b, alg, clock.NewManualClock(benchEpoch))
				claims := benchClaims(b, shape)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := issuer.Issue(claims); err != nil {
						b.Fatalf("Issue: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkVerify prices authenticating one token, per algorithm and per
// claim-set size. This is the hot path: it runs on every authenticated
// request, and it is also the security boundary, so the number and the
// refusal table below have to be read together.
func BenchmarkVerify(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, alg := range benchAlgorithms {
		for _, shape := range []string{"minimal", "realistic"} {
			b.Run(alg+"/"+shape, func(b *testing.B) {
				clk := clock.NewManualClock(benchEpoch)
				minted := benchMint(b, ring.issuerFor(b, alg, clk), benchClaims(b, shape))
				verifier := ring.verifierFor(b, alg, clk)
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
}

// benchMint issues one token or fails the benchmark.
func benchMint(b *testing.B, issuer coretoken.Issuer, claims coretoken.ClaimsValue) string {
	b.Helper()
	minted, err := issuer.Issue(claims)
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	return minted
}

// benchVerifyAccepts asserts the token verifies before it is timed.
//
// A benchmark whose subject silently fails on the first byte measures the
// refusal path and reports it as the acceptance path. Every acceptance row
// below runs this first.
func benchVerifyAccepts(b *testing.B, verifier coretoken.Verifier, token string) {
	b.Helper()
	if _, err := verifier.Verify(token); err != nil {
		b.Fatalf("token does not verify, so this row would measure a refusal: %v", err)
	}
}

// BenchmarkVerifyClockSource prices the one distortion every other row here
// carries: a clock.ManualClock reads a struct field where clock.System calls
// into the runtime's monotonic clock. The delta is what a reader adds back to
// convert any row into a production number.
func BenchmarkVerifyClockSource(b *testing.B) {
	ring := newBenchKeyring(b)
	for name, clk := range map[string]clock.Clock{
		"manual": clock.NewManualClock(benchEpoch),
		"system": clock.System,
	} {
		b.Run(name, func(b *testing.B) {
			minted := benchMint(b, ring.issuerFor(b, "HS256", clk), benchClaims(b, "realistic"))
			verifier := ring.verifierFor(b, "HS256", clk)
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

// benchPrivateClaimToken mints an HS256 token carrying count private claims.
func benchPrivateClaimToken(b *testing.B, ring benchKeyring, clk clock.Clock, count int) string {
	b.Helper()
	claims := coretoken.NewClaimsValue().WithSubject("u")
	for i := range count {
		next, err := claims.WithPrivateRaw("claim_"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			json.RawMessage(`"value-`+strings.Repeat("x", 16)+`"`))
		if err != nil {
			b.Fatalf("WithPrivateRaw: %v", err)
		}
		claims = next
	}
	return benchMint(b, ring.issuerFor(b, "HS256", clk), claims)
}

// BenchmarkVerifyPrivateClaims scales the private-claim count from none to the
// core value's MaxPrivateClaims cap, so the per-claim marginal cost is a
// measured slope rather than an extrapolation from two points.
func BenchmarkVerifyPrivateClaims(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, count := range []int{0, 1, 4, 16, 64} {
		b.Run("n="+itoa(count), func(b *testing.B) {
			clk := clock.NewManualClock(benchEpoch)
			minted := benchPrivateClaimToken(b, ring, clk, count)
			verifier := ring.verifierFor(b, "HS256", clk)
			benchVerifyAccepts(b, verifier, minted)
			b.ReportAllocs()
			b.ReportMetric(float64(len(minted)), "token_bytes")
			b.ResetTimer()
			for range b.N {
				if _, err := verifier.Verify(minted); err != nil {
					b.Fatalf("Verify: %v", err)
				}
			}
		})
	}
}

// itoa renders a small non-negative int without pulling strconv into a file
// that otherwise needs it only for sub-benchmark names.
func itoa(n int) string {
	//: the only zero this is ever asked for.
	if n == 0 {
		return "0"
	}
	var digits []byte
	//: least significant first, then reversed.
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
