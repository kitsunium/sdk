package pbkdf2pw_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/crypto/pbkdf2pw"
)

func TestPasswordHasher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		wantOK   bool
	}{
		{"the correct password verifies", "correct horse", true},
		{"a wrong password is rejected", "Tr0ub4dor", false},
	}
	//: one real stored hash backs every row (hashing is slow — do it once).
	phc, err := pbkdf2pw.PasswordHasher.Hash([]byte("correct horse"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the exported singleton must advertise its canonical algorithm.
			if pbkdf2pw.PasswordHasher.Algorithm() != "pbkdf2-sha256" {
				t.Fatalf("Algorithm()=%q want pbkdf2-sha256", pbkdf2pw.PasswordHasher.Algorithm())
			}
			ok, vErr := pbkdf2pw.PasswordHasher.Verify([]byte(c.password), phc)
			//: a round-trip via the registered singleton matches; a wrong one does not.
			if vErr != nil || ok != c.wantOK {
				t.Errorf("Verify=(%v,%v) want (%v,nil)", ok, vErr, c.wantOK)
			}
			//: a current-policy hash must not be flagged stale.
			if pbkdf2pw.PasswordHasher.NeedsRehash(phc) {
				t.Errorf("NeedsRehash reported a current-policy hash as stale")
			}
		})
	}
}
