package crypto_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeAgreement is a comparable Agreement whose ops return fixed bytes, so the
// registry/dispatch tests need no real cryptography.
type fakeAgreement struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the Agreement port.
var _ crypto.Agreement = (*fakeAgreement)(nil)

func (f fakeAgreement) Algorithm() crypto.Algorithm { return f.name }

func (fakeAgreement) GenerateKey() (pub, priv []byte, err error) {
	return []byte("public"), []byte("private"), nil
}

func (fakeAgreement) Shared(_, _ []byte) (secret []byte, err error) { return []byte("secret"), nil }

// distinctAgreement is a SECOND Agreement type claiming the same Algorithm as a
// fakeAgreement, to exercise the distinct-duplicate conflict.
type distinctAgreement struct{}

func (distinctAgreement) Algorithm() crypto.Algorithm { return "agr-dup" }

func (distinctAgreement) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (distinctAgreement) Shared(_, _ []byte) (secret []byte, err error) { return nil, nil }

// agreementStubErr is a bare error used to provoke the AgreementShared wrap path;
// it avoids errs.Define so the AST audit never treats a fixture as a real
// sentinel.
type agreementStubErr struct{}

func (agreementStubErr) Error() string { return "stub agreement failure" }

// failingAgreement is a comparable Agreement whose Shared returns a stub error,
// to exercise the AgreementFailed wrap path.
type failingAgreement struct {
	name crypto.Algorithm
}

func (f failingAgreement) Algorithm() crypto.Algorithm { return f.name }

func (failingAgreement) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (failingAgreement) Shared(_, _ []byte) (secret []byte, err error) {
	return nil, agreementStubErr{}
}

func TestRegisterAgreement(t *testing.T) {
	type tc struct {
		name string
		arg  fakeAgreement
	}
	tests := []tc{
		{"first scheme registers", fakeAgreement{name: "fa-1"}},
		{"second slot, distinct scheme", fakeAgreement{name: "fa-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterAgreement(c.arg)
		//: RegisterAgreement hands back the scheme and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterAgreement returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupAgreement(c.arg.name); !ok {
			t.Errorf("LookupAgreement(%q) failed after RegisterAgreement", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide agreement registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterAgreementPanics(t *testing.T) {
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil scheme panics", func() { crypto.RegisterAgreement(nil) }},
		{
			"distinct duplicate panics",
			func() {
				crypto.RegisterAgreement(fakeAgreement{name: "agr-dup"})
				//: a DISTINCT scheme under the same name is the hard conflict.
				crypto.RegisterAgreement(distinctAgreement{})
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			//: a missing panic means RegisterAgreement failed to guard the case.
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", c.name)
			}
		}()
		c.run()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterAgreementIdempotent(t *testing.T) {
	type tc struct {
		name string
		algo crypto.Algorithm
	}
	tests := []tc{
		{"same instance re-registers cleanly", "agr-idem-1"},
		{"second name re-registers cleanly", "agr-idem-2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		a := fakeAgreement{name: c.algo}
		crypto.RegisterAgreement(a)
		//: re-registering the SAME instance is a no-op, never a panic.
		crypto.RegisterAgreement(a)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestLookupAgreement(t *testing.T) {
	crypto.RegisterAgreement(fakeAgreement{name: "lk-a"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered scheme resolves", "lk-a", true},
		{"unregistered misses", "absent-zzz-a", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupAgreement reports presence by Algorithm.
		if _, ok := crypto.LookupAgreement(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupAgreement(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableAgreements(t *testing.T) {
	type tc struct {
		name string
		seed crypto.Algorithm
	}
	tests := []tc{
		{"first seeded scheme is listed", "av-a-a"},
		{"second seeded scheme is listed", "av-a-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.RegisterAgreement(fakeAgreement{name: c.seed})
		//: AvailableAgreements must include every registered algorithm.
		if !slices.Contains(crypto.AvailableAgreements(), c.seed) {
			t.Errorf("%s: AvailableAgreements missing %q", c.name, c.seed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestGenerateAgreementKey(t *testing.T) {
	crypto.RegisterAgreement(fakeAgreement{name: "gen-a"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered scheme yields a keypair", "gen-a", false},
		{"unregistered algorithm errors", "ghost-a", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pub, priv, err := crypto.GenerateAgreementKey(c.alg)
		//: the failure arm surfaces UnknownAgreementAlgorithm + nil keys.
		if c.wantErr {
			if pub != nil || priv != nil || !errs.HasCode(err, crypto.CodeUnknownAgreementAlgorithm) {
				t.Errorf("%s: GenerateAgreementKey=(%v,%v,%v) want (nil,nil,UnknownAgreementAlgorithm)", c.name, pub, priv, err)
			}
			return
		}
		//: the success arm returns both key halves.
		if err != nil || len(pub) == 0 || len(priv) == 0 {
			t.Errorf("%s: GenerateAgreementKey=(%d,%d,%v)", c.name, len(pub), len(priv), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAgreementShared(t *testing.T) {
	crypto.RegisterAgreement(fakeAgreement{name: "shr-a"})
	crypto.RegisterAgreement(failingAgreement{name: "shr-fail-a"})
	type tc struct {
		name     string
		alg      crypto.Algorithm
		wantMiss bool
		wantWrap bool
	}
	tests := []tc{
		{"registered scheme derives a secret", "shr-a", false, false},
		{"scheme fault wraps into AgreementFailed", "shr-fail-a", false, true},
		{"unregistered algorithm errors", "ghost-a", true, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		secret, err := crypto.AgreementShared(c.alg, []byte("priv"), []byte("peer"))
		//: the miss arm surfaces UnknownAgreementAlgorithm + nil secret.
		if c.wantMiss {
			if secret != nil || !errs.HasCode(err, crypto.CodeUnknownAgreementAlgorithm) {
				t.Errorf("%s: AgreementShared=(%v,%v) want (nil,UnknownAgreementAlgorithm)", c.name, secret, err)
			}
			return
		}
		//: the wrap arm surfaces AgreementFailed + nil secret, leaking no bytes.
		if c.wantWrap {
			if secret != nil || !errs.HasCode(err, crypto.CodeAgreementFailed) {
				t.Errorf("%s: AgreementShared=(%v,%v) want (nil,AgreementFailed)", c.name, secret, err)
			}
			return
		}
		//: the success arm returns the raw shared secret.
		if err != nil || string(secret) != "secret" {
			t.Errorf("%s: AgreementShared=(%q,%v) want (secret,nil)", c.name, secret, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
