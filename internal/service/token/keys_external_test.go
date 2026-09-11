package token_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// rfc7515A3PrivateJWK is RFC 7515 §A.3.1's ES256 key WITH its private scalar —
// a key somebody else wrote down, so accepting it proves more than this SDK
// agreeing with keys it generated itself.
const rfc7515A3PrivateJWK = `{"kty":"EC","crv":"P-256",` +
	`"x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU",` +
	`"y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0",` +
	`"d":"jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI"}`

// TestAPrivateKeyThatIsNotOneKeyIsRefused pins that an ES256 signing key is
// validated as a KEY and not only as a point.
//
// crypto/ecdsa reads the scalar only when it signs — and as of go1.27 it
// dereferences it there, so a nil D used to pass construction and panic in the
// first Issue — and it signs with D without comparing D·G to the declared
// (X, Y). A struct assembled from two keys therefore used to mint, silently,
// tokens its own public half refuses. Each case is refused where it is built.
//
// MUTATION (2026-09-11): the pre-fix bindP256Private put back whole (HEAD's
// keys.go). Observed all five: `nil scalar: NewES256Issuer = (&{…}, <nil>),
// want KEY_UNSUITABLE`, and likewise zero, negative, at the order, and another
// key's scalar. A probe against that same file confirmed both symptoms: the
// nil-scalar issuer's first Issue panicked (`invalid memory address or nil
// pointer dereference`), and the mismatched one minted a token its own public
// half answered with `[0.2.13.4 SIGNATURE_INVALID]`. Restored; SHA-256 of
// keys.go identical to the fixed file.
//
// MUTATION (2026-09-11): the `priv.D == nil || priv.D.Sign() <= 0` refusal was
// deleted. Observed: the test binary PANICKED — `runtime error: invalid
// memory address or nil pointer dereference` in math/big.(*Int).BitLen, from
// crypto/ecdsa.privateKeyToFIPS via (*PrivateKey).ECDH, from bindP256Private —
// the go1.27 dereference the check exists to get ahead of. Restored; SHA-256
// identical.
//
// MUTATION (2026-09-11): the `!derived.PublicKey().Equal(public)` refusal was
// deleted. Observed, and only this: `another key's scalar: NewES256Issuer =
// (&{…}, <nil>), want KEY_UNSUITABLE`. Restored; SHA-256 identical.
func TestAPrivateKeyThatIsNotOneKeyIsRefused(t *testing.T) {
	t.Parallel()
	own, other := testECKey(t), testECKey(t)
	order := elliptic.P256().Params().N
	for _, tc := range []struct {
		name string
		key  *ecdsa.PrivateKey
	}{
		{"nil scalar", &ecdsa.PrivateKey{PublicKey: own.PublicKey}},
		{"zero scalar", &ecdsa.PrivateKey{PublicKey: own.PublicKey, D: new(big.Int)}},
		{"negative scalar", &ecdsa.PrivateKey{PublicKey: own.PublicKey, D: new(big.Int).Neg(own.D)}},
		{"scalar at the order", &ecdsa.PrivateKey{PublicKey: own.PublicKey, D: new(big.Int).Set(order)}},
		{"another key's scalar", &ecdsa.PrivateKey{PublicKey: own.PublicKey, D: new(big.Int).Set(other.D)}},
	} {
		issuer, err := svctoken.NewES256Issuer(tc.key, svctoken.IssuerConfig{Lifetime: time.Hour})
		if !errs.HasCode(err, coretoken.CodeKeyUnsuitable) || issuer != nil {
			t.Errorf("%s: NewES256Issuer = (%v, %v), want KEY_UNSUITABLE", tc.name, issuer, err)
		}
	}
}

// TestEveryWayToObtainAP256KeyStillIssues is the other side of the refusal: a
// real key from every source the SDK expects one from is bound, mints, and is
// verified by its own public half. A check that compared the two halves in the
// wrong representation would refuse all of them, and no refusal test would
// notice.
//
// MUTATION (2026-09-11): the comparison was written across representations —
// `!priv.PublicKey.Equal(derived.PublicKey())`, an *ecdsa.PublicKey asked
// whether it equals an *ecdh.PublicKey, which is false for every key.
// Observed, once per source: `GenerateKey: NewES256Issuer = [0.2.13.12
// KEY_UNSUITABLE] The key cannot be used for this token algorithm, want
// acceptance`, and the same for x509 SEC 1, x509 PKCS #8, jwk round trip and
// RFC 7515 A.3.1. TestAPrivateKeyThatIsNotOneKeyIsRefused stayed green, which
// is why this test exists. Restored; SHA-256 of keys.go identical.
func TestEveryWayToObtainAP256KeyStillIssues(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(epoch)
	generated := testECKey(t)
	for _, tc := range []struct {
		name string
		key  *ecdsa.PrivateKey
	}{
		{"GenerateKey", generated},
		{"x509 SEC 1", viaSEC1(t, generated)},
		{"x509 PKCS #8", viaPKCS8(t, generated)},
		{"jwk round trip", viaJWK(t, generated)},
		{"RFC 7515 A.3.1", parsedJWK(t, rfc7515A3PrivateJWK)},
	} {
		issuer, err := svctoken.NewES256Issuer(tc.key, svctoken.IssuerConfig{Lifetime: time.Hour, Clock: manual})
		if err != nil {
			t.Errorf("%s: NewES256Issuer = %v, want acceptance", tc.name, err)
			continue
		}
		minted, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
		if err != nil {
			t.Errorf("%s: Issue = %v", tc.name, err)
			continue
		}
		verifier, err := svctoken.NewES256Verifier(&tc.key.PublicKey, svctoken.VerifierConfig{Clock: manual})
		if err != nil {
			t.Errorf("%s: NewES256Verifier = %v", tc.name, err)
			continue
		}
		if _, verr := verifier.Verify(minted); verr != nil {
			t.Errorf("%s: the key's own public half refused its token: %v", tc.name, verr)
		}
	}
}

// viaSEC1 round-trips key through x509's SEC 1 encoding.
func viaSEC1(t *testing.T, key *ecdsa.PrivateKey) *ecdsa.PrivateKey {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	parsed, err := x509.ParseECPrivateKey(der)
	if err != nil {
		t.Fatalf("ParseECPrivateKey: %v", err)
	}
	return parsed
}

// viaPKCS8 round-trips key through x509's PKCS #8 encoding.
func viaPKCS8(t *testing.T, key *ecdsa.PrivateKey) *ecdsa.PrivateKey {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	ec, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("ParsePKCS8PrivateKey returned %T, want *ecdsa.PrivateKey", parsed)
	}
	return ec
}

// viaJWK round-trips key through the SDK's own JWK representation.
func viaJWK(t *testing.T, key *ecdsa.PrivateKey) *ecdsa.PrivateKey {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	asJWK, err := jwk.FromECDSAPrivate(der)
	if err != nil {
		t.Fatalf("FromECDSAPrivate: %v", err)
	}
	return jwkPrivate(t, asJWK)
}

// parsedJWK parses a JWK document carrying "d" into an ECDSA private key.
func parsedJWK(t *testing.T, document string) *ecdsa.PrivateKey {
	t.Helper()
	key, err := jwk.Parse([]byte(document))
	if err != nil {
		t.Fatalf("jwk.Parse: %v", err)
	}
	return jwkPrivate(t, key)
}

// jwkPrivate renders a private EC JWK back into crypto/ecdsa's shape.
func jwkPrivate(t *testing.T, key jwk.KeyValue) *ecdsa.PrivateKey {
	t.Helper()
	der, err := key.ECDSAPrivate()
	if err != nil {
		t.Fatalf("ECDSAPrivate: %v", err)
	}
	parsed, err := x509.ParseECPrivateKey(der)
	if err != nil {
		t.Fatalf("ParseECPrivateKey: %v", err)
	}
	return parsed
}
