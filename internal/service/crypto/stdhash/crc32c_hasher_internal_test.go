package stdhash

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func Test_crc32cHasher_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen crc32c key", "crc32c"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: each scheme must report its frozen canonical key.
			if got := (crc32cHasher{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_crc32cHasher_New(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"yields a 4-byte checksum", 4},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: New() must produce a hash of the algorithm's digest size.
			if got := (crc32cHasher{}).New().Size(); got != c.want {
				t.Errorf("New().Size()=%d want %d", got, c.want)
			}
		})
	}
}
