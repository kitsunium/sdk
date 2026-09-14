package pbkdf2pw_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/crypto/pbkdf2pw"
)

// cheapCorrectHorse is `correct horse` hashed at two rounds. Verify recomputes
// at the cost the STORED STRING carries, so the mismatch arm — whose subject is
// the constant-time compare, not the work factor — walks the same code for
// free. The match arm below deliberately keeps a real 600000-round hash.
const cheapCorrectHorse string = "$pbkdf2-sha256$i=2$c2l4dGVlbi1ieXRlLXNsdA" +
	"$s4DDwNuQQW06ycaTBE7fJoL++C67SFIi1RubjuwEM64"

func TestPasswordHasher(t *testing.T) {
	t.Parallel()
	//: ONE real stored hash at the current policy. This is the only place the
	//: exported singleton pays the production cost, and paying it once is what
	//: makes the round trip a conformance check instead of a self-consistent one.
	phc, err := pbkdf2pw.PasswordHasher.Hash([]byte("correct horse"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	//: the exported singleton must advertise its canonical algorithm.
	if pbkdf2pw.PasswordHasher.Algorithm() != "pbkdf2-sha256" {
		t.Fatalf("Algorithm()=%q want pbkdf2-sha256", pbkdf2pw.PasswordHasher.Algorithm())
	}
	//: a hash minted at the current policy must not be flagged stale.
	if pbkdf2pw.PasswordHasher.NeedsRehash(phc) {
		t.Errorf("NeedsRehash reported a current-policy hash as stale")
	}
	//: a two-round hash IS below the policy, so the ratchet must flag it.
	if !pbkdf2pw.PasswordHasher.NeedsRehash(cheapCorrectHorse) {
		t.Errorf("NeedsRehash failed to flag a below-policy hash as stale")
	}
	tests := []struct {
		name     string
		password string
		phc      string
		wantOK   bool
	}{
		{"the correct password verifies", "correct horse", phc, true},
		{"a wrong password is rejected", "Tr0ub4dor", cheapCorrectHorse, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ok, vErr := pbkdf2pw.PasswordHasher.Verify([]byte(c.password), c.phc)
			//: a round-trip via the registered singleton matches; a wrong one does not.
			if vErr != nil || ok != c.wantOK {
				t.Errorf("Verify=(%v,%v) want (%v,nil)", ok, vErr, c.wantOK)
			}
		})
	}
}
