package crypto_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakePasswordHasher is a comparable PasswordHasher whose ops return fixed
// values, so the registry/dispatch tests need no real password stretching. Its
// Hash emits a PHC string whose id segment equals its Algorithm, so the
// PHC-id-based VerifyPassword dispatch resolves back to it.
type fakePasswordHasher struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the PasswordHasher port.
var _ crypto.PasswordHasher = (*fakePasswordHasher)(nil)

func (f fakePasswordHasher) Algorithm() crypto.Algorithm { return f.name }

func (f fakePasswordHasher) Hash(_ []byte) (phc string, err error) {
	return "$" + string(f.name) + "$fake", nil
}

func (fakePasswordHasher) Verify(_ []byte, _ string) (ok bool, err error) { return true, nil }

func (fakePasswordHasher) NeedsRehash(_ string) (stale bool) { return false }

func TestRegisterPasswordHasher(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakePasswordHasher
	}
	tests := []tc{
		{"first hasher registers", fakePasswordHasher{name: "fp-1"}},
		{"second slot, distinct hasher", fakePasswordHasher{name: "fp-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterPasswordHasher(c.arg)
		//: RegisterPasswordHasher hands back the hasher and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterPasswordHasher returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupPasswordHasher(c.arg.name); !ok {
			t.Errorf("LookupPasswordHasher(%q) failed after registration", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide password registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterPasswordHasherPanics(t *testing.T) {
	//: clean slate so the conflict panics on its OWN second registration.
	crypto.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil hasher panics", func() { crypto.RegisterPasswordHasher(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				crypto.RegisterPasswordHasher(fakePasswordHasher{name: "dup-p"})
				//: a DISTINCT hasher under the same name is the hard conflict.
				crypto.RegisterPasswordHasher(distinctPasswordHasher{})
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means registration failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", c.name)
				}
			}()
			c.run()
		})
	}
}

// distinctPasswordHasher is a SECOND type claiming the same Name as a
// fakePasswordHasher, to exercise the distinct-duplicate conflict.
type distinctPasswordHasher struct{}

func (distinctPasswordHasher) Algorithm() crypto.Algorithm { return "dup-p" }

func (distinctPasswordHasher) Hash(_ []byte) (phc string, err error) { return "", nil }

func (distinctPasswordHasher) Verify(_ []byte, _ string) (ok bool, err error) { return false, nil }

func (distinctPasswordHasher) NeedsRehash(_ string) (stale bool) { return false }

func TestLookupPasswordHasher(t *testing.T) {
	//: sequential — seeds + reads the process-wide password registry.
	crypto.ResetForTest()
	crypto.RegisterPasswordHasher(fakePasswordHasher{name: "lk-p"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered hasher resolves", "lk-p", true},
		{"unregistered misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupPasswordHasher reports presence by Algorithm.
		if _, ok := crypto.LookupPasswordHasher(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupPasswordHasher(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailablePasswordHashers(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []crypto.Algorithm
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two hashers listed sorted ascending", []crypto.Algorithm{"av-p-b", "av-p-a"}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.ResetForTest()
		//: seed the registry with the row's hashers before listing.
		for _, name := range c.seed {
			crypto.RegisterPasswordHasher(fakePasswordHasher{name: name})
		}
		got := crypto.AvailablePasswordHashers()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: AvailablePasswordHashers()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded hasher, sorted.
		if len(got) != c.wantLen || got[0] != crypto.Algorithm("av-p-a") || got[1] != crypto.Algorithm("av-p-b") {
			t.Errorf("%s: AvailablePasswordHashers()=%v want [av-p-a av-p-b]", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestHashPassword(t *testing.T) {
	//: sequential — seeds + reads the process-wide password registry.
	crypto.ResetForTest()
	crypto.RegisterPasswordHasher(fakePasswordHasher{name: "hash-p"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered hasher produces a PHC string", "hash-p", false},
		{"unregistered algorithm errors", "ghost-p", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		phc, err := crypto.HashPassword(c.alg, []byte("password"))
		//: the failure arm surfaces UnknownPasswordAlgorithm.
		if c.wantErr {
			if phc != "" || !errs.HasCode(err, crypto.CodeUnknownPasswordAlgorithm) {
				t.Errorf("%s: HashPassword=(%q,%v) want (\"\",UnknownPasswordAlgorithm)", c.name, phc, err)
			}
			return
		}
		//: the success arm returns the scheme's PHC string.
		if err != nil || phc == "" {
			t.Errorf("%s: HashPassword=(%q,%v)", c.name, phc, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestVerifyPassword(t *testing.T) {
	//: sequential — seeds + reads the process-wide password registry.
	crypto.ResetForTest()
	crypto.RegisterPasswordHasher(fakePasswordHasher{name: "vfy-p"})
	type tc struct {
		name    string
		phc     string
		wantOK  bool
		wantErr bool
	}
	tests := []tc{
		{"registered scheme verifies via the PHC id", "$vfy-p$fake", true, false},
		{"malformed PHC is InvalidPasswordHash", "not-a-phc", false, true},
		{"unknown PHC scheme errors", "$ghost-p$fake", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ok, err := crypto.VerifyPassword([]byte("password"), c.phc)
		//: the failure arms surface a typed error, ok=false.
		if c.wantErr {
			if ok || err == nil {
				t.Errorf("%s: VerifyPassword=(%v,%v) want (false, error)", c.name, ok, err)
			}
			return
		}
		//: a registered scheme reports validity with no error channel.
		if err != nil || ok != c.wantOK {
			t.Errorf("%s: VerifyPassword=(%v,%v) want (%v,nil)", c.name, ok, err, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestNeedsRehash(t *testing.T) {
	//: sequential — seeds + reads the process-wide password registry.
	crypto.ResetForTest()
	crypto.RegisterPasswordHasher(fakePasswordHasher{name: "nr-p"})
	type tc struct {
		name string
		phc  string
	}
	tests := []tc{
		{"known scheme delegates (fake reports fresh)", "$nr-p$fake"},
		{"malformed PHC is never stale", "not-a-phc"},
		{"unknown scheme is never stale", "$ghost-p$fake"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the fake reports not-stale; malformed/unknown also report not-stale.
		if crypto.NeedsRehash(c.phc) {
			t.Errorf("%s: NeedsRehash(%q)=true want false", c.name, c.phc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
