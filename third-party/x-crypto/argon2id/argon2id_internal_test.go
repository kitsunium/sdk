package argon2id

import (
	"strings"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_argon2idPW_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen argon2id key", "argon2id"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key (also the PHC id).
			if got := (argon2idPW{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_argon2idPW_Hash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		prefix string
	}{
		{"emits a PHC string at the current policy", "$argon2id$v=19$m=19456,t=2,p=1$"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			phc, err := (argon2idPW{}).Hash([]byte("password"))
			//: a fresh hash must carry the scheme id, version, and current costs.
			if err != nil || !strings.HasPrefix(phc, c.prefix) {
				t.Fatalf("Hash=(%q,%v) want prefix %q", phc, err, c.prefix)
			}
			//: and it must be parseable by the scheme's own decoder.
			if _, _, _, _, _, ok := decodePHC(phc); !ok {
				t.Errorf("Hash produced an undecodable PHC: %q", phc)
			}
		})
	}
}

func Test_argon2idPW_Verify(t *testing.T) {
	t.Parallel()
	//: a real stored hash backs the match/mismatch rows (one Hash — argon2 is slow).
	good, herr := (argon2idPW{}).Hash([]byte("secret"))
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
		{"malformed PHC is InvalidPasswordHash", "secret", "$argon2id$v=19$nope", false, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ok, vErr := (argon2idPW{}).Verify([]byte(c.password), c.phc)
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

func Test_argon2idPW_NeedsRehash(t *testing.T) {
	t.Parallel()
	//: a current-policy hash backs the fresh row.
	fresh, herr := (argon2idPW{}).Hash([]byte("pw"))
	if herr != nil {
		t.Fatalf("Hash: %v", herr)
	}
	tests := []struct {
		name string
		phc  string
		want bool
	}{
		{"current-policy hash is fresh", fresh, false},
		//: m=8,t=1,p=1 is far below policy → stale (16-byte salt + 32-byte digest).
		{"weak-parameter hash is stale", "$argon2id$v=19$m=8,t=1,p=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", true},
		{"malformed PHC is never stale", "not-a-phc", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: staleness is any cost below current policy; malformed reports false.
			if got := (argon2idPW{}).NeedsRehash(c.phc); got != c.want {
				t.Errorf("NeedsRehash(%q)=%v want %v", c.phc, got, c.want)
			}
		})
	}
}

func Test_encodePHC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mem  uint32
	}{
		{"round-trips through decodePHC", 19456},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			salt := []byte("sixteen-byte-slt")
			digest := []byte("thirty-two-byte-digest-padding!!")
			phc := encodePHC(salt, digest, c.mem, 2, 1)
			gm, gt, gp, gs, gd, ok := decodePHC(phc)
			//: encode then decode must reproduce every field exactly.
			if !ok || gm != c.mem || gt != 2 || gp != 1 || string(gs) != string(salt) || string(gd) != string(digest) {
				t.Errorf("round-trip mismatch: (%d,%d,%d,%q,%q,%v)", gm, gt, gp, gs, gd, ok)
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
		{"well-formed parses", "$argon2id$v=19$m=8,t=1,p=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", true},
		{"wrong field count fails", "$argon2id$v=19$m=8,t=1,p=1$only", false},
		{"wrong id fails", "$other$v=19$m=8,t=1,p=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", false},
		{"wrong version fails", "$argon2id$v=99$m=8,t=1,p=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", false},
		{"bad params field fails", "$argon2id$v=19$m=8,t=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", false},
		{"bad base64 salt fails", "$argon2id$v=19$m=8,t=1,p=1$!!!$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", false},
		//: a salt that is not exactly 16 bytes is corruption → reject.
		{"wrong salt length fails", "$argon2id$v=19$m=8,t=1,p=1$ZmlmdGVlbi1ieXRlLXNs$dGhpcnR5LXR3by1ieXRlLWRpZ2VzdC1wYWRkaW5nISE", false},
		//: a digest that is not exactly 32 bytes is corruption → reject.
		{"wrong digest length fails", "$argon2id$v=19$m=8,t=1,p=1$c2l4dGVlbi1ieXRlLXNsdA$dGhpcnR5LW9uZS1ieXRlLWRpZ2VzdC1wYWRkaW5nIQ", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: only a fully validated PHC string returns ok=true.
			if _, _, _, _, _, ok := decodePHC(c.phc); ok != c.wantOK {
				t.Errorf("decodePHC(%q) ok=%v want %v", c.phc, ok, c.wantOK)
			}
		})
	}
}

func Test_parseParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		field  string
		wantOK bool
	}{
		{"valid m,t,p parses", "m=19456,t=2,p=1", true},
		{"wrong order key fails", "t=2,m=19456,p=1", false},
		{"too few parameters fails", "m=19456,t=2", false},
		{"non-numeric value fails", "m=abc,t=2,p=1", false},
		//: p=256 would wrap the uint8 cast to 0 → reject above the 255 ceiling.
		{"parallelism above 255 fails", "m=19456,t=2,p=256", false},
		//: m above the 2 GiB cap is a resource-exhaustion attempt → reject.
		{"memory above cap fails", "m=2097153,t=2,p=1", false},
		//: t above the cap is an unbounded-work attempt → reject.
		{"time above cap fails", "m=19456,t=1048577,p=1", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: only the canonical m,t,p triple parses.
			if _, _, _, ok := parseParams(c.field); ok != c.wantOK {
				t.Errorf("parseParams(%q) ok=%v want %v", c.field, ok, c.wantOK)
			}
		})
	}
}
