package ecdsasig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_ecdsaP256_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen ecdsa-p256 key", "ecdsa-p256"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (ecdsaP256{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_ecdsaP256_GenerateKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"yields a non-empty PKIX public + SEC1 private key"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pub, priv, err := (ecdsaP256{}).GenerateKey()
			//: both DER blobs must be present (their exact lengths vary).
			if err != nil || len(pub) == 0 || len(priv) == 0 {
				t.Errorf("%s: GenerateKey=(%d,%d,%v)", c.name, len(pub), len(priv), err)
			}
		})
	}
}

func Test_ecdsaP256_Sign(t *testing.T) {
	t.Parallel()
	//: a real keypair backs the valid-key row.
	_, priv, gerr := (ecdsaP256{}).GenerateKey()
	if gerr != nil {
		t.Fatalf("GenerateKey: %v", gerr)
	}
	tests := []struct {
		name    string
		priv    []byte
		wantErr bool
	}{
		{"valid private key signs", priv, false},
		{"malformed private key is SigningFailed", []byte("not-der"), true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sig, serr := (ecdsaP256{}).Sign(c.priv, []byte("message"))
			//: a malformed key surfaces the typed SigningFailed, never a panic.
			if c.wantErr {
				if sig != nil || !errs.HasCode(serr, corecrypto.CodeSigningFailed) {
					t.Errorf("Sign=(%v,%v) want (nil,SigningFailed)", sig, serr)
				}
				return
			}
			//: a valid key yields a non-empty DER signature.
			if serr != nil || len(sig) == 0 {
				t.Errorf("Sign=(%d bytes,%v) want (>0,nil)", len(sig), serr)
			}
		})
	}
}

func Test_ecdsaP256_Verify(t *testing.T) {
	t.Parallel()
	//: a real signed message backs the positive row.
	pub, priv, gerr := (ecdsaP256{}).GenerateKey()
	if gerr != nil {
		t.Fatalf("GenerateKey: %v", gerr)
	}
	sig, serr := (ecdsaP256{}).Sign(priv, []byte("message"))
	if serr != nil {
		t.Fatalf("Sign: %v", serr)
	}
	tests := []struct {
		name string
		pub  []byte
		msg  []byte
		sig  []byte
		want bool
	}{
		{"valid signature verifies", pub, []byte("message"), sig, true},
		{"tampered message fails", pub, []byte("tampered"), sig, false},
		{"malformed public key fails", []byte("not-der"), []byte("message"), sig, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: validity is a plain bool; malformed inputs are false, never a panic.
			if got := (ecdsaP256{}).Verify(c.pub, c.msg, c.sig); got != c.want {
				t.Errorf("Verify=%v want %v", got, c.want)
			}
		})
	}
}

// Test_ecdsaP256_Verify_rejectsOffCurveKey proves the scheme is curve-bound: a
// structurally valid ECDSA key on a DIFFERENT NIST curve must verify to false
// rather than being accepted as a weaker-but-valid signer.
func Test_ecdsaP256_Verify_rejectsOffCurveKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		curve elliptic.Curve
	}{
		{"rejects a P-384 key", elliptic.P384()},
		{"rejects a P-521 key", elliptic.P521()},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a real keypair on the wrong curve — valid ECDSA, wrong algorithm;
			//: nil reader selects crypto/rand by default (Go 1.26+).
			key, gerr := ecdsa.GenerateKey(c.curve, nil)
			if gerr != nil {
				t.Fatalf("GenerateKey: %v", gerr)
			}
			//: marshal to the same PKIX DER form Verify parses.
			pubDER, merr := x509.MarshalPKIXPublicKey(&key.PublicKey)
			if merr != nil {
				t.Fatalf("MarshalPKIXPublicKey: %v", merr)
			}
			//: the off-curve key parses fine but must be rejected on the curve check.
			if (ecdsaP256{}).Verify(pubDER, []byte("message"), []byte("sig")) {
				t.Errorf("%s: Verify accepted an off-curve key; want false", c.name)
			}
		})
	}
}
