package entitlement

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"testing"
)

// Test_digestHelpers_areTheAlgorithmsTheirCallersNeed pins each digest helper
// to the exact algorithm its caller's format names, against the standard
// library's own implementation.
//
// The Roughtime suite cannot do this on its own: its fixtures build their Merkle
// paths with hashRoughtimeLeaf and hashRoughtimeNode, so a helper hashing with
// the wrong algorithm builds wrong fixtures and checks them consistently — seen
// passing with SHA-256 swapped in for SHA-512. A real Roughtime server computes
// its tree with SHA-512, and only a known answer can tell the two apart.
func Test_digestHelpers_areTheAlgorithmsTheirCallersNeed(t *testing.T) {
	t.Parallel()

	inputs := []struct {
		name string
		data []byte
	}{
		{name: "no bytes", data: nil},
		{name: "a short message", data: []byte("roughtime")},
		{name: "a leaf-prefixed nonce", data: append([]byte{roughtimeLeafPrefix}, bytes.Repeat([]byte{0xA5}, 64)...)},
		{name: "a payload larger than one block", data: bytes.Repeat([]byte("roster"), 300)},
	}

	for _, in := range inputs {
		t.Run(in.name, func(t *testing.T) {
			t.Parallel()

			wantTree := sha512.Sum512(in.data)
			if got := sha512Prefix(in.data, roughtimeHashSize); !bytes.Equal(got, wantTree[:roughtimeHashSize]) {
				t.Errorf("sha512Prefix(%q) = %x, want the first %d bytes of SHA-512, %x",
					in.name, got, roughtimeHashSize, wantTree[:roughtimeHashSize])
			}
			if got, want := payloadDigest(in.data), sha256.Sum256(in.data); got != want {
				t.Errorf("payloadDigest(%q) = %x, want SHA-256, %x", in.name, got, want)
			}
		})
	}
}

// Test_verifiedBy pins the signature check every authenticated document in this
// package goes through: a genuine signature verifies, and every other shape —
// a tampered message, another key, a malformed key or signature — answers false
// rather than panicking, which is what the facade promises and what the length
// checks at the call sites no longer have to guard against.
func Test_verifiedBy(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	message := []byte("a roster the vendor signed")
	signature := ed25519.Sign(priv, message)

	tests := []struct {
		name      string
		key       []byte
		message   []byte
		signature []byte
		want      bool
	}{
		{name: "the genuine signature verifies", key: pub, message: message, signature: signature, want: true},
		{name: "a tampered message does not", key: pub, message: []byte("a roster somebody else wrote"), signature: signature, want: false},
		{name: "another key does not", key: other, message: message, signature: signature, want: false},
		{name: "a short key is false, not a panic", key: pub[:16], message: message, signature: signature, want: false},
		{name: "a short signature is false, not a panic", key: pub, message: message, signature: signature[:32], want: false},
		{name: "no key at all is false", key: nil, message: message, signature: signature, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := verifiedBy(tt.key, tt.message, tt.signature); got != tt.want {
				t.Errorf("verifiedBy = %v, want %v", got, tt.want)
			}
		})
	}
}
