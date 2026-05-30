package stdhash

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func Test_sha512Hasher_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen sha512 key", "sha512"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: each scheme must report its frozen canonical key.
			if got := (sha512Hasher{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_sha512Hasher_New(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"yields a 64-byte digest", 64},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: New() must produce a hash of the algorithm's digest size.
			if got := (sha512Hasher{}).New().Size(); got != c.want {
				t.Errorf("New().Size()=%d want %d", got, c.want)
			}
		})
	}
}
