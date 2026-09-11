package token_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/token"
)

// sinks so no issued or verified token can be proven unused and elided.
var (
	strSink    string
	claimsSink token.Claims
	errSink    error
)

// benchSecret is the 32-byte symmetric key the HS256 benchmarks share.
// token.Key is a struct with a defensive copy, not a []byte, so it is built
// through its constructor rather than with make.
func benchSecret(b *testing.B) token.Key {
	b.Helper()
	key, err := crypto.NewKey(make([]byte, 32))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	return key
}

// benchClaims builds the claim set every benchmark below issues: a realistic
// access token, not an empty one — an empty ClaimsValue would measure the
// signature and nothing of the JSON the signature covers.
func benchClaims() token.Claims {
	return token.NewClaims().
		WithIssuer("https://issuer.example").
		WithSubject("user-01HQ8Z3M4N5P6Q7R8S9T0V1W2X").
		WithAudience("https://api.example").
		WithID("01HQ8Z3M4N5P6Q7R8S9T0V1W2X").
		WithIssuedAt(time.Now()).
		WithExpiry(time.Now().Add(time.Hour))
}

func benchIssuerConfig() token.IssuerConfig {
	return token.IssuerConfig{Issuer: "https://issuer.example", Lifetime: time.Hour}
}

func benchVerifierConfig() token.VerifierConfig {
	return token.VerifierConfig{Issuer: "https://issuer.example", Audience: "https://api.example"}
}

// BenchmarkIssue_* and BenchmarkVerify_* are the point of this file. Verifying
// runs once per authenticated request and issuing once per login, so the two
// have very different budgets — and the three JWS algorithms differ by more
// than an order of magnitude, which is the fact a caller needs before choosing.
func BenchmarkIssue_HS256(b *testing.B) {
	issuer, err := token.NewHS256Issuer(benchSecret(b), benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewHS256Issuer: %v", err)
	}
	claims := benchClaims()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = issuer.Issue(claims)
	}
}

func BenchmarkVerify_HS256(b *testing.B) {
	secret := benchSecret(b)
	issuer, err := token.NewHS256Issuer(secret, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewHS256Issuer: %v", err)
	}
	verifier, err := token.NewHS256Verifier(secret, benchVerifierConfig())
	if err != nil {
		b.Fatalf("NewHS256Verifier: %v", err)
	}
	signed, err := issuer.Issue(benchClaims())
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(signed)
	}
}

func BenchmarkIssue_ES256(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewES256Issuer(priv, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewES256Issuer: %v", err)
	}
	claims := benchClaims()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = issuer.Issue(claims)
	}
}

func BenchmarkVerify_ES256(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewES256Issuer(priv, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewES256Issuer: %v", err)
	}
	verifier, err := token.NewES256Verifier(&priv.PublicKey, benchVerifierConfig())
	if err != nil {
		b.Fatalf("NewES256Verifier: %v", err)
	}
	signed, err := issuer.Issue(benchClaims())
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(signed)
	}
}

func BenchmarkIssue_EdDSA(b *testing.B) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewEdDSAIssuer(priv, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewEdDSAIssuer: %v", err)
	}
	claims := benchClaims()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = issuer.Issue(claims)
	}
}

func BenchmarkVerify_EdDSA(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewEdDSAIssuer(priv, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewEdDSAIssuer: %v", err)
	}
	verifier, err := token.NewEdDSAVerifier(pub, benchVerifierConfig())
	if err != nil {
		b.Fatalf("NewEdDSAVerifier: %v", err)
	}
	signed, err := issuer.Issue(benchClaims())
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(signed)
	}
}

// BenchmarkIssue_PasetoV4 and BenchmarkVerify_PasetoV4 are the other format.
// They use the same Ed25519 primitive as EdDSA above, so the delta between the
// two pairs is what the ENVELOPE costs — JWS compact against PASETO — with the
// cryptography held constant.
func BenchmarkIssue_PasetoV4(b *testing.B) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewPasetoV4Issuer(priv, token.PasetoIssuerConfig{
		Issuer: "https://issuer.example", Lifetime: time.Hour,
	})
	if err != nil {
		b.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	claims := benchClaims()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = issuer.Issue(claims)
	}
}

func BenchmarkVerify_PasetoV4(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	issuer, err := token.NewPasetoV4Issuer(priv, token.PasetoIssuerConfig{
		Issuer: "https://issuer.example", Lifetime: time.Hour,
	})
	if err != nil {
		b.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	verifier, err := token.NewPasetoV4Verifier(pub, token.PasetoVerifierConfig{
		Issuer: "https://issuer.example", Audience: "https://api.example",
	})
	if err != nil {
		b.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	signed, err := issuer.Issue(benchClaims())
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(signed)
	}
}

// BenchmarkVerify_HS256_Tampered is the REFUSAL path, and it matters for the
// same reason it did in core/trace: a verifier is exposed to input the caller
// does not choose, so a refusal that costs far more than an acceptance is an
// amplification. A tampered signature must be rejected without costing more
// than accepting a good one.
func BenchmarkVerify_HS256_Tampered(b *testing.B) {
	secret := benchSecret(b)
	issuer, err := token.NewHS256Issuer(secret, benchIssuerConfig())
	if err != nil {
		b.Fatalf("NewHS256Issuer: %v", err)
	}
	verifier, err := token.NewHS256Verifier(secret, benchVerifierConfig())
	if err != nil {
		b.Fatalf("NewHS256Verifier: %v", err)
	}
	signed, err := issuer.Issue(benchClaims())
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	//: flip the last signature byte — the cheapest tamper that must still fail.
	tampered := signed[:len(signed)-1] + "A"
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(tampered)
	}
	if errSink == nil {
		b.Fatal("a tampered token verified")
	}
}

// BenchmarkVerify_HS256_Oversize pins the bound check: MaxTokenLen is checked
// BEFORE the work it funds (the CVE-2025-30204 class), so an absurd input must
// be refused for almost nothing.
func BenchmarkVerify_HS256_Oversize(b *testing.B) {
	verifier, err := token.NewHS256Verifier(benchSecret(b), token.VerifierConfig{
		Issuer: "https://issuer.example", Audience: "https://api.example", MaxTokenLen: 4096,
	})
	if err != nil {
		b.Fatalf("NewHS256Verifier: %v", err)
	}
	oversize := make([]byte, 64*1024)
	for i := range oversize {
		oversize[i] = 'A'
	}
	huge := string(oversize)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		claimsSink, errSink = verifier.Verify(huge)
	}
	if errSink == nil {
		b.Fatal("an oversize token verified")
	}
}

// BenchmarkNewClaims measures the builder alone, so the issue numbers above can
// be read without wondering how much of them is JSON assembly.
func BenchmarkNewClaims(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		claimsSink = benchClaims()
	}
}
