package token_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
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

// TestJWKFacadeIsWired covers the two key-set entry points.
func TestJWKFacadeIsWired(t *testing.T) {
	t.Parallel()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	parsed, err := jwk.FromECDSAPublic(der)
	if err != nil {
		t.Fatalf("FromECDSAPublic: %v", err)
	}
	key := parsed.WithKid("k1")
	issuer, err := token.NewES256Issuer(ec, token.IssuerConfig{Lifetime: time.Hour, KeyID: "k1"})
	if err != nil {
		t.Fatalf("NewES256Issuer: %v", err)
	}
	minted, err := issuer.Issue(token.NewClaims().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	single, err := token.NewVerifierFromJWK(key, token.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewVerifierFromJWK: %v", err)
	}
	if _, verr := single.Verify(minted); verr != nil {
		t.Fatalf("single-key JWK verify: %v", verr)
	}
	set, err := token.NewSetVerifier(jwk.NewSet(key), token.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewSetVerifier: %v", err)
	}
	if _, verr := set.Verify(minted); verr != nil {
		t.Fatalf("set verify: %v", verr)
	}
}
