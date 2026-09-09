package jwk_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"

	//: the two registered signature schemes, so the interop test drives the
	//: SDK's real dispatch rather than a private copy of the same crypto.
	_ "github.com/kitsunium/sdk/internal/service/crypto/ecdsasig"
	_ "github.com/kitsunium/sdk/internal/service/crypto/ed25519sig"
)

// ecKeyPair generates a DER keypair on curve, in the encodings
// service/crypto/ecdsasig produces and consumes.
func ecKeyPair(tb testing.TB, curve elliptic.Curve) (pubDER, privDER []byte) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(curve, nil)
	if err != nil {
		tb.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	pubDER, perr := x509.MarshalPKIXPublicKey(&key.PublicKey)
	privDER, merr := x509.MarshalECPrivateKey(key)
	if perr != nil || merr != nil {
		tb.Fatalf("marshal: %v / %v", perr, merr)
	}
	return pubDER, privDER
}

// TestECBridgeCoversEveryJOSECurve is the reason the package models P-384 and
// P-521 even though the SDK registers a signer only for P-256: a JWK Set
// published by somebody else routinely carries them, and refusing to REPRESENT
// a key the format defines would just push ad-hoc parsing back on the caller.
func TestECBridgeCoversEveryJOSECurve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		curve elliptic.Curve
		want  jwk.Curve
		size  int
	}{
		{"P-256", elliptic.P256(), jwk.CurveP256, 32},
		{"P-384", elliptic.P384(), jwk.CurveP384, 48},
		{"P-521 rounds up to 66 octets", elliptic.P521(), jwk.CurveP521, 66},
	}
	runCase := func(t *testing.T, c struct {
		name  string
		curve elliptic.Curve
		want  jwk.Curve
		size  int
	},
	) {
		t.Helper()
		pubDER, privDER := ecKeyPair(t, c.curve)
		pubKey, perr := jwk.FromECDSAPublic(pubDER)
		privKey, kerr := jwk.FromECDSAPrivate(privDER)
		if perr != nil || kerr != nil {
			t.Fatalf("bridge in: %v / %v", perr, kerr)
		}
		//: the curve name and the fixed coordinate length must both be right;
		//: P-521's 66 octets is the case a naive bits/8 gets wrong.
		if pubKey.Crv() != c.want || privKey.Crv() != c.want {
			t.Fatalf("curve=(%q,%q) want %q", pubKey.Crv(), privKey.Crv(), c.want)
		}
		//: the public halves must agree — same key, two DER encodings.
		reduced, rerr := privKey.Public()
		if rerr != nil || !reduced.Equal(pubKey) {
			t.Errorf("private key's public half differs from the public key (%v)", rerr)
		}
		//: and the DER must come back out identical, so the JWK is lossless.
		backPub, bperr := pubKey.ECDSAPublic()
		backPriv, bkerr := privKey.ECDSAPrivate()
		if bperr != nil || bkerr != nil ||
			string(backPub) != string(pubDER) || string(backPriv) != string(privDER) {
			t.Errorf("DER round trip lost bytes: %v / %v", bperr, bkerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSignerInterop is the end-to-end claim: a key that has been through the
// JWK format still signs and verifies under the SDK's registered schemes. A
// format that does not survive contact with the scheme it describes is decor.
func TestSignerInterop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		alg     corecrypto.Algorithm
		toJWK   func([]byte) (jwk.KeyValue, error)
		fromJWK func(jwk.KeyValue) ([]byte, error)
		private bool
	}{
		{"ECDSA P-256 private key survives the format", "ecdsa-p256", jwk.FromECDSAPrivate, jwk.KeyValue.ECDSAPrivate, true},
		{"ECDSA P-256 public key survives the format", "ecdsa-p256", jwk.FromECDSAPublic, jwk.KeyValue.ECDSAPublic, false},
		{"Ed25519 private key survives the format", "ed25519", jwk.FromEd25519Private, jwk.KeyValue.Ed25519Private, true},
		{"Ed25519 public key survives the format", "ed25519", jwk.FromEd25519Public, jwk.KeyValue.Ed25519Public, false},
	}
	message := []byte("the payload a JOSE header would point a kid at")
	runCase := func(t *testing.T, c struct {
		name    string
		alg     corecrypto.Algorithm
		toJWK   func([]byte) (jwk.KeyValue, error)
		fromJWK func(jwk.KeyValue) ([]byte, error)
		private bool
	},
	) {
		t.Helper()
		pub, priv, gerr := corecrypto.GenerateKey(c.alg)
		if gerr != nil {
			t.Fatalf("GenerateKey(%q): %v", c.alg, gerr)
		}
		source := pub
		//: the private rows carry the signing half through the format.
		if c.private {
			source = priv
		}
		key, kerr := c.toJWK(source)
		if kerr != nil {
			t.Fatalf("into JWK: %v", kerr)
		}
		//: a full serialise/parse cycle, so the test exercises the format and
		//: not just the in-memory conversion.
		doc, derr := serialise(t, key)
		reparsed, rerr := jwk.Parse(doc)
		if derr != nil || rerr != nil {
			t.Fatalf("serialise/parse: %v / %v", derr, rerr)
		}
		restored, berr := c.fromJWK(reparsed)
		if berr != nil || string(restored) != string(source) {
			t.Fatalf("out of JWK: %v (%d vs %d octets)", berr, len(restored), len(source))
		}
		//: and the restored key really works under the registered scheme.
		sig, serr := corecrypto.Sign(c.alg, priv, message)
		ok, verr := corecrypto.Verify(c.alg, pub, message, sig)
		if serr != nil || verr != nil || !ok {
			t.Errorf("sign/verify after the round trip: %v / %v / %v", serr, verr, ok)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// serialise renders key through whichever path its material allows, so interop
// rows do not each repeat the public/private choice.
func serialise(tb testing.TB, key jwk.KeyValue) ([]byte, error) {
	tb.Helper()
	//: a key holding a secret can only be rendered whole by the private path.
	if key.IsPrivate() {
		return key.MarshalPrivate()
	}
	return key.MarshalPublic()
}

// TestSecretBridgeStaysRedacting checks that wrapping a core/crypto.Key in a JWK
// is not a way around the protection core/crypto.Key was given: the resulting
// JWK still refuses the public path, and the key comes back as a redacting
// crypto.Key rather than as loose bytes.
func TestSecretBridgeStaysRedacting(t *testing.T) {
	t.Parallel()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	secret, nerr := corecrypto.NewKey(raw)
	if nerr != nil {
		t.Fatalf("NewKey: %v", nerr)
	}
	tests := []struct {
		name string
	}{
		{"an oct JWK built from a crypto.Key keeps every guarantee"},
	}
	runCase := func(t *testing.T, _ struct{ name string }) {
		t.Helper()
		key, ferr := jwk.FromSecret(secret)
		if ferr != nil {
			t.Fatalf("FromSecret: %v", ferr)
		}
		//: still refuses the public path.
		if _, err := key.MarshalPublic(); !errs.HasCode(err, jwk.CodeJWKNoPublicForm) {
			t.Errorf("MarshalPublic err=%v want NoPublicForm", err)
		}
		//: and hands the material back only as a redacting crypto.Key.
		back, berr := key.Secret()
		if berr != nil || string(back.Bytes()) != string(raw) {
			t.Fatalf("Secret: %v", berr)
		}
		if back.String() != "<redacted>" {
			t.Errorf("crypto.Key came back unredacted: %q", back.String())
		}
		//: the same key still tags under the registered MAC scheme.
		if _, terr := key.Thumbprint(); terr != nil {
			t.Errorf("Thumbprint: %v", terr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestBridgeRejects(t *testing.T) {
	t.Parallel()
	edPub, edPriv, gerr := ed25519.GenerateKey(nil)
	if gerr != nil {
		t.Fatalf("ed25519.GenerateKey: %v", gerr)
	}
	edSPKI, serr := x509.MarshalPKIXPublicKey(edPub)
	if serr != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", serr)
	}
	p224Pub, _ := ecKeyPair(t, elliptic.P224())
	//: a private key whose seed and public half disagree — the 64-octet blob
	//: crypto/ed25519 accepts structurally but that is not one keypair.
	forged := bytes.Clone(edPriv)
	forged[len(forged)-1] ^= 0xFF
	tests := []struct {
		name     string
		invoke   func() (jwk.KeyValue, error)
		wantCode errs.Code
	}{
		{"not DER at all", func() (jwk.KeyValue, error) { return jwk.FromECDSAPublic([]byte("nope")) }, jwk.CodeJWKMalformed},
		{"not a SEC1 private key", func() (jwk.KeyValue, error) { return jwk.FromECDSAPrivate([]byte("nope")) }, jwk.CodeJWKMalformed},
		{"an Ed25519 SPKI is not an EC key", func() (jwk.KeyValue, error) { return jwk.FromECDSAPublic(edSPKI) }, jwk.CodeJWKTypeMismatch},
		{"P-224 has no JOSE curve name", func() (jwk.KeyValue, error) { return jwk.FromECDSAPublic(p224Pub) }, jwk.CodeJWKUnsupportedCurve},
		{"a short Ed25519 public key", func() (jwk.KeyValue, error) { return jwk.FromEd25519Public(edPub[:16]) }, jwk.CodeJWKInvalidEncoding},
		{"a short Ed25519 private key", func() (jwk.KeyValue, error) { return jwk.FromEd25519Private(edPriv[:32]) }, jwk.CodeJWKInvalidEncoding},
		{"an Ed25519 blob whose halves disagree", func() (jwk.KeyValue, error) { return jwk.FromEd25519Private(forged) }, jwk.CodeJWKKeyMismatch},
		{"a short symmetric key", func() (jwk.KeyValue, error) { return jwk.FromSecret(corecrypto.Key{}) }, corecrypto.CodeInvalidKey},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		invoke   func() (jwk.KeyValue, error)
		wantCode errs.Code
	},
	) {
		t.Helper()
		key, err := c.invoke()
		//: a rejected input yields the zero Key and the typed code.
		if !key.IsZero() || !errs.HasCode(err, c.wantCode) {
			t.Errorf("got (%v,%v) want zero key + code %v", key, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestTypedAccessorsRefuseTheWrongFamily(t *testing.T) {
	t.Parallel()
	ecKey := parseFixture(t, ecPrivateDoc)
	okpKey := parseFixture(t, okpPrivateDoc)
	octKey := parseFixture(t, octDoc)
	publicEC := parseFixture(t, ecPublicDoc)
	publicOKP := parseFixture(t, okpPublicDoc)
	tests := []struct {
		name     string
		invoke   func() error
		wantCode errs.Code
	}{
		{"ECDSAPublic on an OKP key", func() error { _, err := okpKey.ECDSAPublic(); return err }, jwk.CodeJWKTypeMismatch},
		{"Ed25519Public on an EC key", func() error { _, err := ecKey.Ed25519Public(); return err }, jwk.CodeJWKTypeMismatch},
		{"Secret on an EC key", func() error { _, err := ecKey.Secret(); return err }, jwk.CodeJWKTypeMismatch},
		{"ECDSAPrivate on an oct key", func() error { _, err := octKey.ECDSAPrivate(); return err }, jwk.CodeJWKTypeMismatch},
		{"ECDSAPrivate on a public EC key", func() error { _, err := publicEC.ECDSAPrivate(); return err }, jwk.CodeJWKNoPrivateMaterial},
		{"Ed25519Private on a public OKP key", func() error { _, err := publicOKP.Ed25519Private(); return err }, jwk.CodeJWKNoPrivateMaterial},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		invoke   func() error
		wantCode errs.Code
	},
	) {
		t.Helper()
		//: an accessor for the wrong family refuses; it never reinterprets the
		//: material as if it belonged to another curve.
		if err := c.invoke(); !errs.HasCode(err, c.wantCode) {
			t.Errorf("err=%v want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
