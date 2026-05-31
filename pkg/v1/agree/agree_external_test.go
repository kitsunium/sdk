package agree_test

import (
	"bytes"
	"testing"

	agree "github.com/kitsunium/sdk/pkg/v1/agree"
	"github.com/kitsunium/sdk/pkg/v1/crypto"
)

func TestAgree_twoPartySharedKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		info string
	}
	tests := []tc{
		{"both parties derive the same usable AEAD key", "app-v1"},
		{"a different info still agrees between the pair", "app-v2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each party draws an ephemeral keypair.
		pubA, privA, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey A: %v", err)
		}
		pubB, privB, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey B: %v", err)
		}
		//: A uses its private + B's public; B mirrors with the swap + same info.
		keyA, aerr := agree.SharedKey(agree.X25519, privA, pubB, c.info)
		if aerr != nil {
			t.Fatalf("SharedKey A: %v", aerr)
		}
		keyB, berr := agree.SharedKey(agree.X25519, privB, pubA, c.info)
		if berr != nil {
			t.Fatalf("SharedKey B: %v", berr)
		}
		//: the derived keys must be usable AEAD keys that interoperate: A seals,
		//: B opens — only equal keys can do this round-trip.
		box, serr := crypto.Seal(keyA, []byte("hello peer"), nil)
		if serr != nil {
			t.Fatalf("Seal under keyA: %v", serr)
		}
		got, oerr := crypto.Open(keyB, box, nil)
		if oerr != nil || !bytes.Equal(got, []byte("hello peer")) {
			t.Errorf("Open under keyB=(%q,%v) want (hello peer,nil) — keys disagree", got, oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgree_badPeerKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		peerPub []byte
	}
	tests := []tc{
		{"all-zero low-order peer point fails", make([]byte, 32)},
		{"garbage-length peer key fails", []byte{0x1, 0x2}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, priv, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		//: a low-order/garbage peer key must fail rather than yield a key.
		key, aerr := agree.SharedKey(agree.X25519, priv, c.peerPub, "ctx")
		if aerr == nil {
			t.Errorf("SharedKey accepted a bad peer key (got key %v)", key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgree_unknownAlgorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"an unregistered scheme fails GenerateKey"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a scheme no package registered must not resolve.
			if _, _, err := agree.GenerateKey("p384-not-registered"); err == nil {
				t.Errorf("GenerateKey resolved an unregistered scheme")
			}
		})
	}
}
