package pbkdf2pw

import (
	"strings"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_pbkdf2PW_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen pbkdf2-sha256 key", "pbkdf2-sha256"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key (also the PHC id).
			if got := (pbkdf2PW{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_pbkdf2PW_Hash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		prefix string
	}{
		{"emits a PHC string at the current policy", "$pbkdf2-sha256$i=600000$"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			phc, err := (pbkdf2PW{}).Hash([]byte("password"))
			//: a fresh hash must carry the scheme id + current iteration count.
			if err != nil || !strings.HasPrefix(phc, c.prefix) {
				t.Fatalf("Hash=(%q,%v) want prefix %q", phc, err, c.prefix)
			}
			//: and it must be parseable by the scheme's own decoder.
			if _, _, _, ok := decodePHC(phc); !ok {
				t.Errorf("Hash produced an undecodable PHC: %q", phc)
			}
		})
	}
}

func Test_pbkdf2PW_Verify(t *testing.T) {
	t.Parallel()
	//: a real stored hash backs the match/mismatch rows.
	good, herr := (pbkdf2PW{}).Hash([]byte("secret"))
	if herr != nil {
		t.Fatalf("Hash: %v", herr)
	}
	tests := []struct {
		name     string
		password string
		phc      string
		wantOK   bool
		wantErr  bool
	}{
		{"correct password verifies", "secret", good, true, false},
		{"wrong password is rejected", "wrong", good, false, false},
		{"malformed PHC is InvalidPasswordHash", "secret", "$pbkdf2-sha256$nope", false, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ok, vErr := (pbkdf2PW{}).Verify([]byte(c.password), c.phc)
			//: a malformed stored hash surfaces InvalidPasswordHash, ok=false.
			if c.wantErr {
				if ok || !errs.HasCode(vErr, corecrypto.CodeInvalidPasswordHash) {
					t.Errorf("Verify=(%v,%v) want (false,InvalidPasswordHash)", ok, vErr)
				}
				return
			}
			//: a genuine mismatch is (false,nil); a match is (true,nil).
			if vErr != nil || ok != c.wantOK {
				t.Errorf("Verify=(%v,%v) want (%v,nil)", ok, vErr, c.wantOK)
			}
		})
	}
}

func Test_pbkdf2PW_NeedsRehash(t *testing.T) {
	t.Parallel()
	//: a current-policy hash backs the fresh row.
	fresh, herr := (pbkdf2PW{}).Hash([]byte("pw"))
	if herr != nil {
		t.Fatalf("Hash: %v", herr)
	}
	tests := []struct {
		name string
		phc  string
		want bool
	}{
		{"current-policy hash is fresh", fresh, false},
		//: i=1 is far below the 600000 policy → stale (valid base64 salt/digest).
		{"low-iteration hash is stale", "$pbkdf2-sha256$i=1$c2FsdA$ZGlnZXN0", true},
		{"malformed PHC is never stale", "not-a-phc", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: staleness is iters < current policy; malformed reports false.
			if got := (pbkdf2PW{}).NeedsRehash(c.phc); got != c.want {
				t.Errorf("NeedsRehash(%q)=%v want %v", c.phc, got, c.want)
			}
		})
	}
}

func Test_encodePHC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		iters int
	}{
		{"round-trips through decodePHC", 600000},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			salt := []byte("sixteen-byte-slt")
			digest := []byte("thirty-two-byte-digest-padding!!")
			phc := encodePHC(salt, digest, c.iters)
			gi, gs, gd, ok := decodePHC(phc)
			//: encode then decode must reproduce every field exactly.
			if !ok || gi != c.iters || string(gs) != string(salt) || string(gd) != string(digest) {
				t.Errorf("round-trip mismatch: (%d,%q,%q,%v)", gi, gs, gd, ok)
			}
		})
	}
}

func Test_decodePHC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		phc    string
		wantOK bool
	}{
		{"well-formed parses", "$pbkdf2-sha256$i=5$c2FsdA$ZGlnZXN0", true},
		{"wrong field count fails", "$pbkdf2-sha256$i=5$only", false},
		{"wrong id fails", "$other$i=5$c2FsdA$ZGlnZXN0", false},
		{"missing i= label fails", "$pbkdf2-sha256$5$c2FsdA$ZGlnZXN0", false},
		{"non-numeric iters fails", "$pbkdf2-sha256$i=abc$c2FsdA$ZGlnZXN0", false},
		{"bad base64 salt fails", "$pbkdf2-sha256$i=5$!!!$ZGlnZXN0", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: only a fully validated PHC string returns ok=true.
			if _, _, _, ok := decodePHC(c.phc); ok != c.wantOK {
				t.Errorf("decodePHC(%q) ok=%v want %v", c.phc, ok, c.wantOK)
			}
		})
	}
}
