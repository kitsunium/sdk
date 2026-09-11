package token_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/base64"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/token"
)

// TestEveryConstructorIsWired walks the whole facade. Each delegating
// constructor is a one-liner, and a one-liner pointing at the wrong service
// function still compiles — minting and verifying through every one of them is
// the cheapest proof that none of them does.
func TestEveryConstructorIsWired(t *testing.T) {
	t.Parallel()
	secret, err := crypto.NewKey(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	issueCfg := token.IssuerConfig{Lifetime: time.Hour, KeyID: "k1"}
	verifyCfg := token.VerifierConfig{}
	pasetoIssue := token.PasetoIssuerConfig{Lifetime: time.Hour}
	pasetoVerify := token.PasetoVerifierConfig{}

	for name, pair := range map[string]struct {
		issuer   func() (token.Issuer, error)
		verifier func() (token.Verifier, error)
	}{
		"HS256": {
			func() (token.Issuer, error) { return token.NewHS256Issuer(secret, issueCfg) },
			func() (token.Verifier, error) { return token.NewHS256Verifier(secret, verifyCfg) },
		},
		"ES256": {
			func() (token.Issuer, error) { return token.NewES256Issuer(ec, issueCfg) },
			func() (token.Verifier, error) { return token.NewES256Verifier(&ec.PublicKey, verifyCfg) },
		},
		"EdDSA": {
			func() (token.Issuer, error) { return token.NewEdDSAIssuer(edPriv, issueCfg) },
			func() (token.Verifier, error) { return token.NewEdDSAVerifier(edPub, verifyCfg) },
		},
		"PASETO v4.public": {
			func() (token.Issuer, error) { return token.NewPasetoV4Issuer(edPriv, pasetoIssue) },
			func() (token.Verifier, error) { return token.NewPasetoV4Verifier(edPub, pasetoVerify) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			issuer, err := pair.issuer()
			if err != nil {
				t.Fatalf("issuer: %v", err)
			}
			verifier, err := pair.verifier()
			if err != nil {
				t.Fatalf("verifier: %v", err)
			}
			minted, err := issuer.Issue(token.NewClaims().WithSubject("u"))
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if _, verr := verifier.Verify(minted); verr != nil {
				t.Fatalf("Verify: %v", verr)
			}
		})
	}
}

// TestJWKFacadeIsWired covers the five JWK entry points: the three loaders and
// the two verifiers built from what they load. Its key reaches the facade the
// only way a consumer's can — as a JWK document, parsed by ParseJWK — because
// this file imports nothing under internal/. It used to import
// internal/service/crypto/jwk to build that key, which is how a facade whose
// JWK constructors took an argument no consumer could build passed its own
// wiring test.
//
// MUTATION (2026-09-11): NewJWKSet was made to return `JWKSet{}`, dropping the
// keys it was handed. Observed: `construct: [0.2.13.13 POLICY_MISCONFIGURED]
// The token policy is misconfigured`, on the NewSetVerifier(NewJWKSet) row —
// this is the only test in the package that calls NewJWKSet. Restored;
// constructors.go's SHA-256 is byte-identical to the pre-mutation one.
func TestJWKFacadeIsWired(t *testing.T) {
	t.Parallel()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	document := publicJWK(t, &ec.PublicKey, "k1")
	key, err := token.ParseJWK(document)
	if err != nil {
		t.Fatalf("ParseJWK: %v", err)
	}
	parsed, err := token.ParseJWKSet([]byte(`{"keys":[` + string(document) + `]}`))
	if err != nil {
		t.Fatalf("ParseJWKSet: %v", err)
	}
	issuer, err := token.NewES256Issuer(ec, token.IssuerConfig{Lifetime: time.Hour, KeyID: "k1"})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	minted, err := issuer.Issue(token.NewClaims().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for name, build := range map[string]func() (token.Verifier, error){
		"NewVerifierFromJWK(ParseJWK)": func() (token.Verifier, error) {
			return token.NewVerifierFromJWK(key, token.VerifierConfig{})
		},
		"NewSetVerifier(NewJWKSet)": func() (token.Verifier, error) {
			return token.NewSetVerifier(token.NewJWKSet(key), token.VerifierConfig{})
		},
		"NewSetVerifier(ParseJWKSet)": func() (token.Verifier, error) {
			return token.NewSetVerifier(parsed, token.VerifierConfig{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, berr := build()
			if berr != nil {
				t.Fatalf("construct: %v", berr)
			}
			if _, verr := verifier.Verify(minted); verr != nil {
				t.Fatalf("Verify: %v", verr)
			}
		})
	}
}

// publicJWK renders pub as the public JWK document an issuer publishes under
// kid — by hand, from the SEC 1 uncompressed point, exactly as a publisher
// outside this SDK would. The kid is a test literal and the base64url alphabet
// needs no JSON escaping, so concatenation is the whole encoder.
func publicJWK(t *testing.T, pub *ecdsa.PublicKey, kid string) []byte {
	t.Helper()
	point, err := pub.Bytes() // 0x04 || X || Y, each coordinate 32 octets on P-256
	if err != nil {
		t.Fatalf("PublicKey.Bytes: %v", err)
	}
	x := base64.RawURLEncoding.EncodeToString(point[1:33])
	y := base64.RawURLEncoding.EncodeToString(point[33:65])
	return []byte(`{"kty":"EC","crv":"P-256","kid":"` + kid + `","x":"` + x + `","y":"` + y + `"}`)
}
