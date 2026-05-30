package ecdsasig_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/crypto/ecdsasig"
)

func TestSigner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		tamper bool
		want   bool
	}{
		{"genuine signature verifies", false, true},
		{"tampered message is rejected", true, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := ecdsasig.Signer
			//: the exported singleton must advertise its canonical algorithm.
			if s.Algorithm() != "ecdsa-p256" {
				t.Fatalf("Algorithm()=%q want ecdsa-p256", s.Algorithm())
			}
			pub, priv, err := s.GenerateKey()
			if err != nil {
				t.Fatalf("GenerateKey: %v", err)
			}
			sig, err := s.Sign(priv, []byte("message"))
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			//: the tamper arm verifies a different message than the one signed.
			msg := []byte("message")
			if c.tamper {
				msg = []byte("tampered")
			}
			//: a round-trip via the registered singleton verifies; a tamper does not.
			if got := s.Verify(pub, msg, sig); got != c.want {
				t.Errorf("Verify=%v want %v", got, c.want)
			}
		})
	}
}
