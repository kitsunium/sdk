package hkdfsha256_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)

func TestDeriver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		infoA  string
		infoB  string
		differ bool
	}{
		{"distinct info labels give independent subkeys", "aead-key", "mac-key", true},
		{"identical info labels are deterministic", "same", "same", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := hkdfsha256.Deriver
			//: the exported singleton must advertise its canonical algorithm.
			if d.Algorithm() != "hkdf-sha256" {
				t.Fatalf("Algorithm()=%q want hkdf-sha256", d.Algorithm())
			}
			a, err := d.Derive([]byte("master"), []byte("salt"), c.infoA, 32)
			if err != nil {
				t.Fatalf("Derive A: %v", err)
			}
			b, err := d.Derive([]byte("master"), []byte("salt"), c.infoB, 32)
			if err != nil {
				t.Fatalf("Derive B: %v", err)
			}
			//: distinct info must separate keys; identical info must reproduce.
			if bytes.Equal(a, b) == c.differ {
				t.Errorf("Derive(%q) vs Derive(%q): equal=%v, want differ=%v", c.infoA, c.infoB, bytes.Equal(a, b), c.differ)
			}
		})
	}
}
