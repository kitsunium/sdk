package hkdfsha256

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_hkdfSHA256_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen hkdf-sha256 key", "hkdf-sha256"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (hkdfSHA256{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_hkdfSHA256_Derive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		length  int
		wantErr bool
	}{
		{"32-byte subkey derives", 32, false},
		//: HKDF-SHA256's ceiling is 255*32 = 8160 bytes; one over must fail.
		{"over-long length is DerivationFailed", 255*32 + 1, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := (hkdfSHA256{}).Derive([]byte("secret"), []byte("salt"), "info", c.length)
			//: an over-long request surfaces the typed DerivationFailed, no bytes.
			if c.wantErr {
				if got != nil || !errs.HasCode(err, corecrypto.CodeDerivationFailed) {
					t.Errorf("Derive=(%v,%v) want (nil,DerivationFailed)", got, err)
				}
				return
			}
			//: a valid request returns exactly length bytes.
			if err != nil || len(got) != c.length {
				t.Errorf("Derive=(%d bytes,%v) want (%d,nil)", len(got), err, c.length)
			}
		})
	}
}
