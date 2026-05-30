package ed25519sig

import (
	"crypto/ed25519"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_ed25519Signer_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen ed25519 key", "ed25519"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (ed25519Signer{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_ed25519Signer_GenerateKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		wantPub  int
		wantPriv int
	}{
		{"yields a 32-byte public and 64-byte private key", ed25519.PublicKeySize, ed25519.PrivateKeySize},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pub, priv, err := (ed25519Signer{}).GenerateKey()
			//: a fresh keypair must have the scheme's fixed key sizes.
			if err != nil || len(pub) != c.wantPub || len(priv) != c.wantPriv {
				t.Errorf("GenerateKey=(%d,%d,%v) want (%d,%d,nil)", len(pub), len(priv), err, c.wantPub, c.wantPriv)
			}
		})
	}
}

func Test_ed25519Signer_Sign(t *testing.T) {
	t.Parallel()
	//: a real keypair backs the valid-key row.
	_, priv, gerr := (ed25519Signer{}).GenerateKey()
	if gerr != nil {
		t.Fatalf("GenerateKey: %v", gerr)
	}
	tests := []struct {
		name    string
		priv    []byte
		wantErr bool
	}{
		{"valid private key signs", priv, false},
		{"short private key is SigningFailed", []byte("too-short"), true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sig, serr := (ed25519Signer{}).Sign(c.priv, []byte("message"))
			//: a malformed key surfaces the typed SigningFailed, never a panic.
			if c.wantErr {
				if sig != nil || !errs.HasCode(serr, corecrypto.CodeSigningFailed) {
					t.Errorf("Sign=(%v,%v) want (nil,SigningFailed)", sig, serr)
				}
				return
			}
			//: a valid key yields a 64-byte Ed25519 signature.
			if serr != nil || len(sig) != ed25519.SignatureSize {
				t.Errorf("Sign=(%d bytes,%v) want (64,nil)", len(sig), serr)
			}
		})
	}
}

func Test_ed25519Signer_Verify(t *testing.T) {
	t.Parallel()
	//: a real signed message backs the positive row.
	pub, priv, gerr := (ed25519Signer{}).GenerateKey()
	if gerr != nil {
		t.Fatalf("GenerateKey: %v", gerr)
	}
	sig, serr := (ed25519Signer{}).Sign(priv, []byte("message"))
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
		{"short public key fails", []byte("short"), []byte("message"), sig, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: validity is a plain bool; malformed inputs are false, never a panic.
			if got := (ed25519Signer{}).Verify(c.pub, c.msg, c.sig); got != c.want {
				t.Errorf("Verify=%v want %v", got, c.want)
			}
		})
	}
}
