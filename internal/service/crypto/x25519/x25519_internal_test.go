package x25519

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func Test_x25519Agreement_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen x25519 key", "x25519"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (x25519Agreement{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_x25519Agreement_Shared(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		priv    []byte
		peerPub []byte
	}{
		{"short private key fails", make([]byte, 4), make([]byte, 32)},
		{"short peer key fails", make([]byte, 32), make([]byte, 4)},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a malformed input must error rather than return a degenerate secret.
			got, err := (x25519Agreement{}).Shared(c.priv, c.peerPub)
			if err == nil || got != nil {
				t.Errorf("Shared=(%v,%v) want (nil,err)", got, err)
			}
		})
	}
}

func Test_x25519Agreement_GenerateKey_sizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"a fresh keypair has 32-byte halves"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pub, priv, err := (x25519Agreement{}).GenerateKey()
			//: a healthy host yields a 32-byte public and a 32-byte private key.
			if err != nil || len(pub) != 32 || len(priv) != 32 {
				t.Fatalf("GenerateKey=(%d,%d,%v) want (32,32,nil)", len(pub), len(priv), err)
			}
			//: the two halves must not be identical (sanity, not a crypto check).
			if bytes.Equal(pub, priv) {
				t.Errorf("public and private key bytes are identical")
			}
		})
	}
}
