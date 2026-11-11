package aesgcm

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// Test_aesGCM_Bind pins #813's bound sealer: SealTo appends after what dst
// holds the framing Seal writes — the version, the id, a fresh nonce — so
// Open opens it under the same key and aad, two boxes never share a nonce,
// and a dst with the room is extended in place.
func Test_aesGCM_Bind(t *testing.T) {
	t.Parallel()
	key := mustKey(t, 0x7)
	sealer, err := aesGCM{}.Bind(key)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	plaintext, aad := []byte("a record"), []byte("ctx")
	dst := make([]byte, 3, 3+headerLen+nonceLen+len(plaintext)+gcmTagLen)
	copy(dst, "pfx")
	out, err := sealer.SealTo(dst, plaintext, aad)
	if err != nil {
		t.Fatalf("SealTo: %v", err)
	}
	if &out[0] != &dst[0] || string(out[:3]) != "pfx" {
		t.Fatalf("SealTo did not extend dst in place after its prefix")
	}
	box := out[3:]
	if box[0] != corecrypto.Version || box[1] != algID || len(box) != headerLen+nonceLen+len(plaintext)+gcmTagLen {
		t.Fatalf("box header=[%#x %#x] len=%d, want Seal's framing", box[0], box[1], len(box))
	}
	if got, oerr := (aesGCM{}).Open(key, box, aad); oerr != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Open(SealTo's box) = (%q, %v), want %q", got, oerr, plaintext)
	}
	again, err := sealer.SealTo(nil, plaintext, aad)
	if err != nil || bytes.Equal(again[headerLen:headerLen+nonceLen], box[headerLen:headerLen+nonceLen]) {
		t.Fatalf("a second SealTo err=%v or the same nonce", err)
	}
	via, err := corecrypto.NewSealer(algorithm, key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if _, bound := via.(boundGCM); !bound {
		t.Fatalf("NewSealer(%q) = %T, want the bound GCM", algorithm, via)
	}
	if _, err := (aesGCM{}).Bind(corecrypto.Key{}); err == nil {
		t.Fatal("Bind(zero Key) = nil error, want InvalidKey")
	}
}
