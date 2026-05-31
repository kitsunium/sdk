package x25519_test

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/x25519"
)

func TestAgreement_twoParty(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"two parties derive the identical shared secret through the registry"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: importing the package self-registered it, so the registry resolves.
		if x25519.Agreement.Algorithm() != corecrypto.Algorithm("x25519") {
			t.Fatalf("Algorithm()=%q want x25519", x25519.Agreement.Algorithm())
		}
		//: each party draws an ephemeral keypair via the core dispatcher.
		pubA, privA, err := corecrypto.GenerateAgreementKey("x25519")
		if err != nil {
			t.Fatalf("GenerateAgreementKey A: %v", err)
		}
		pubB, privB, err := corecrypto.GenerateAgreementKey("x25519")
		if err != nil {
			t.Fatalf("GenerateAgreementKey B: %v", err)
		}
		//: A computes with its private + B's public; B mirrors with the swap.
		secretA, aerr := corecrypto.AgreementShared("x25519", privA, pubB)
		if aerr != nil {
			t.Fatalf("AgreementShared A: %v", aerr)
		}
		secretB, berr := corecrypto.AgreementShared("x25519", privB, pubA)
		if berr != nil {
			t.Fatalf("AgreementShared B: %v", berr)
		}
		//: the DH contract: both parties must arrive at the same 32-byte secret.
		if !bytes.Equal(secretA, secretB) || len(secretA) != 32 {
			t.Errorf("shared secrets differ or wrong length: A=%x B=%x", secretA, secretB)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgreement_lowOrderPeer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		peerPub []byte
	}
	tests := []tc{
		//: the all-zero point is low-order; crypto/ecdh must reject it.
		{"all-zero peer point is AgreementFailed", make([]byte, 32)},
		//: a wrong-length peer key is malformed; also AgreementFailed.
		{"garbage-length peer key is AgreementFailed", []byte{0x1, 0x2, 0x3}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, priv, err := corecrypto.GenerateAgreementKey("x25519")
		if err != nil {
			t.Fatalf("GenerateAgreementKey: %v", err)
		}
		//: a low-order/garbage peer point wraps into the typed AgreementFailed.
		secret, aerr := corecrypto.AgreementShared("x25519", priv, c.peerPub)
		if secret != nil || !errs.HasCode(aerr, corecrypto.CodeAgreementFailed) {
			t.Errorf("AgreementShared=(%x,%v) want (nil,AgreementFailed)", secret, aerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgreement_unknownAlgorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"an unregistered agreement algorithm surfaces UnknownAgreementAlgorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a name no package registered must surface the typed sentinel.
			_, _, err := corecrypto.GenerateAgreementKey("p256-not-registered")
			if !errs.HasCode(err, corecrypto.CodeUnknownAgreementAlgorithm) {
				t.Errorf("GenerateAgreementKey err=%v want UnknownAgreementAlgorithm", err)
			}
		})
	}
}
