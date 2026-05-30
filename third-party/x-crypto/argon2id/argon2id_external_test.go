package argon2id_test

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/third-party/x-crypto/argon2id"
)

func TestPasswordHasher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		wantOK   bool
	}{
		{"the correct password verifies", "correct horse", true},
		{"a wrong password is rejected", "battery staple", false},
	}
	//: one real stored hash backs every row (argon2 is memory-hard + slow).
	phc, err := argon2id.PasswordHasher.Hash([]byte("correct horse"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the exported singleton must advertise its canonical algorithm.
			if argon2id.PasswordHasher.Algorithm() != "argon2id" {
				t.Fatalf("Algorithm()=%q want argon2id", argon2id.PasswordHasher.Algorithm())
			}
			//: importing this package registers argon2id, so the core dispatcher
			//: routes VerifyPassword by the PHC id ("argon2id") back to this scheme.
			ok, vErr := corecrypto.VerifyPassword([]byte(c.password), phc)
			//: a genuine mismatch is (false,nil); a match is (true,nil).
			if vErr != nil || ok != c.wantOK {
				t.Errorf("VerifyPassword=(%v,%v) want (%v,nil)", ok, vErr, c.wantOK)
			}
		})
	}
}
