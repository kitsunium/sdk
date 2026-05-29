package aesgcm_test

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
)

func TestAEAD(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the registered scheme seals + opens through the core registry"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the exported singleton must report the canonical algorithm + wire id.
		if aesgcm.AEAD.Algorithm() != corecrypto.Algorithm("aes-256-gcm") || aesgcm.AEAD.ID() != 0x01 {
			t.Fatalf("Algorithm()=%q ID()=%#x", aesgcm.AEAD.Algorithm(), aesgcm.AEAD.ID())
		}
		key, err := corecrypto.NewKey(bytes.Repeat([]byte{0x9}, corecrypto.KeyLen))
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		//: importing the package self-registered it, so the registry resolves.
		box, serr := corecrypto.Seal("aes-256-gcm", key, []byte("hi"), []byte("ctx"))
		if serr != nil {
			t.Fatalf("Seal: %v", serr)
		}
		//: the box opens back to the plaintext under the same key + aad.
		if got, oerr := corecrypto.Open(key, box, []byte("ctx")); oerr != nil || !bytes.Equal(got, []byte("hi")) {
			t.Errorf("Open=(%q,%v) want (hi,nil)", got, oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
