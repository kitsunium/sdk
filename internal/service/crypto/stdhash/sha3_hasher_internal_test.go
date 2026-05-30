package stdhash

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func Test_sha3Hasher_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen sha3-256 key", "sha3-256"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: each scheme must report its frozen canonical key.
			if got := (sha3Hasher{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_sha3Hasher_New(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"yields a 32-byte digest", 32},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: New() must produce a hash of the algorithm's digest size.
			if got := (sha3Hasher{}).New().Size(); got != c.want {
				t.Errorf("New().Size()=%d want %d", got, c.want)
			}
		})
	}
}
