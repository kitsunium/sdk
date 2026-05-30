package password_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/password"
)

func TestHash(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     password.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"pbkdf2-sha256 produces a PHC string", password.PBKDF2SHA256, false},
		{"unknown algorithm errors", "no-such-scheme", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		phc, err := password.Hash(c.alg, []byte("correct horse"))
		//: the failure arm must error and yield no hash.
		if c.wantErr {
			if err == nil || phc != "" {
				t.Errorf("%s: Hash accepted an unregistered scheme", c.name)
			}
			return
		}
		//: a registered scheme returns a PHC string tagged with its id.
		if err != nil || !strings.HasPrefix(phc, "$pbkdf2-sha256$") {
			t.Errorf("%s: Hash=(%q,%v)", c.name, phc, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	//: one real stored hash backs the match/mismatch rows.
	good, err := password.Hash(password.PBKDF2SHA256, []byte("correct horse"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	type tc struct {
		name    string
		input   string
		phc     string
		wantOK  bool
		wantErr bool
	}
	tests := []tc{
		{"correct password verifies", "correct horse", good, true, false},
		{"wrong password is rejected without error", "wrong", good, false, false},
		{"malformed stored hash errors", "correct horse", "garbage", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ok, vErr := password.Verify([]byte(c.input), c.phc)
		//: a malformed stored hash surfaces an error, ok=false.
		if c.wantErr {
			if ok || vErr == nil {
				t.Errorf("%s: Verify=(%v,%v) want (false, error)", c.name, ok, vErr)
			}
			return
		}
		//: a genuine mismatch is (false,nil); a match is (true,nil).
		if vErr != nil || ok != c.wantOK {
			t.Errorf("%s: Verify=(%v,%v) want (%v,nil)", c.name, ok, vErr, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()
	//: a current-policy hash backs the fresh row.
	fresh, err := password.Hash(password.PBKDF2SHA256, []byte("pw"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	type tc struct {
		name string
		phc  string
		want bool
	}
	tests := []tc{
		{"a current-policy hash is not stale", fresh, false},
		//: i=1 is far below policy → stale (valid base64 salt/digest fields).
		{"a low-iteration hash is stale", "$pbkdf2-sha256$i=1$c2FsdA$ZGlnZXN0", true},
		{"a malformed hash is never stale", "garbage", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: stale hashes (below policy) must be flagged for upgrade-on-verify.
		if got := password.NeedsRehash(c.phc); got != c.want {
			t.Errorf("%s: NeedsRehash(%q)=%v want %v", c.name, c.phc, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
