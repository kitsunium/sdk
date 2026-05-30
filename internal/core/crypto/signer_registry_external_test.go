package crypto_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeSigner is a comparable Signer whose ops return fixed bytes, so the
// registry/dispatch tests need no real cryptography.
type fakeSigner struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the Signer port.
var _ crypto.Signer = (*fakeSigner)(nil)

func (f fakeSigner) Algorithm() crypto.Algorithm { return f.name }

func (fakeSigner) GenerateKey() (pub, priv []byte, err error) {
	return []byte("public"), []byte("private"), nil
}

func (fakeSigner) Sign(_, _ []byte) (sig []byte, err error) { return []byte("signature"), nil }

func (fakeSigner) Verify(_, _, _ []byte) (ok bool) { return true }

func TestRegisterSigner(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakeSigner
	}
	tests := []tc{
		{"first signer registers", fakeSigner{name: "fs-1"}},
		{"second slot, distinct signer", fakeSigner{name: "fs-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterSigner(c.arg)
		//: RegisterSigner hands back the signer and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterSigner returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupSigner(c.arg.name); !ok {
			t.Errorf("LookupSigner(%q) failed after RegisterSigner", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide signer registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterSignerPanics(t *testing.T) {
	//: clean slate so the conflict panics on its OWN second registration.
	crypto.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil signer panics", func() { crypto.RegisterSigner(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				crypto.RegisterSigner(fakeSigner{name: "dup-s"})
				//: a DISTINCT signer under the same name is the hard conflict.
				crypto.RegisterSigner(distinctSigner{})
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means RegisterSigner failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", c.name)
				}
			}()
			c.run()
		})
	}
}

// distinctSigner is a SECOND Signer type claiming the same Name as a fakeSigner,
// to exercise the distinct-duplicate conflict (a different dynamic type).
type distinctSigner struct{}

func (distinctSigner) Algorithm() crypto.Algorithm { return "dup-s" }

func (distinctSigner) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (distinctSigner) Sign(_, _ []byte) (sig []byte, err error) { return nil, nil }

func (distinctSigner) Verify(_, _, _ []byte) (ok bool) { return false }

func TestLookupSigner(t *testing.T) {
	//: sequential — seeds + reads the process-wide signer registry.
	crypto.ResetForTest()
	crypto.RegisterSigner(fakeSigner{name: "lk-s"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered signer resolves", "lk-s", true},
		{"unregistered misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupSigner reports presence by Algorithm.
		if _, ok := crypto.LookupSigner(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupSigner(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableSigners(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []crypto.Algorithm
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two signers listed sorted ascending", []crypto.Algorithm{"av-s-b", "av-s-a"}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.ResetForTest()
		//: seed the registry with the row's signers before listing.
		for _, name := range c.seed {
			crypto.RegisterSigner(fakeSigner{name: name})
		}
		got := crypto.AvailableSigners()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: AvailableSigners()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded signer, sorted.
		if len(got) != c.wantLen || got[0] != crypto.Algorithm("av-s-a") || got[1] != crypto.Algorithm("av-s-b") {
			t.Errorf("%s: AvailableSigners()=%v want [av-s-a av-s-b]", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestGenerateKey(t *testing.T) {
	//: sequential — seeds + reads the process-wide signer registry.
	crypto.ResetForTest()
	crypto.RegisterSigner(fakeSigner{name: "gen-s"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered signer yields a keypair", "gen-s", false},
		{"unregistered algorithm errors", "ghost-s", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pub, priv, err := crypto.GenerateKey(c.alg)
		//: the failure arm surfaces UnknownSignatureAlgorithm + nil keys.
		if c.wantErr {
			if pub != nil || priv != nil || !errs.HasCode(err, crypto.CodeUnknownSignatureAlgorithm) {
				t.Errorf("%s: GenerateKey=(%v,%v,%v) want (nil,nil,UnknownSignatureAlgorithm)", c.name, pub, priv, err)
			}
			return
		}
		//: the success arm returns both key halves.
		if err != nil || len(pub) == 0 || len(priv) == 0 {
			t.Errorf("%s: GenerateKey=(%d,%d,%v)", c.name, len(pub), len(priv), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSign(t *testing.T) {
	//: sequential — seeds + reads the process-wide signer registry.
	crypto.ResetForTest()
	crypto.RegisterSigner(fakeSigner{name: "sign-s"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered signer produces a signature", "sign-s", false},
		{"unregistered algorithm errors", "ghost-s", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sig, err := crypto.Sign(c.alg, []byte("priv"), []byte("message"))
		//: the failure arm surfaces UnknownSignatureAlgorithm.
		if c.wantErr {
			if sig != nil || !errs.HasCode(err, crypto.CodeUnknownSignatureAlgorithm) {
				t.Errorf("%s: Sign=(%v,%v) want (nil,UnknownSignatureAlgorithm)", c.name, sig, err)
			}
			return
		}
		//: the success arm returns the scheme's signature bytes.
		if err != nil || len(sig) == 0 {
			t.Errorf("%s: Sign=(%d bytes,%v)", c.name, len(sig), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestVerify(t *testing.T) {
	//: sequential — seeds + reads the process-wide signer registry.
	crypto.ResetForTest()
	crypto.RegisterSigner(fakeSigner{name: "vfy-s"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantOK  bool
		wantErr bool
	}
	tests := []tc{
		{"registered signer reports validity", "vfy-s", true, false},
		{"unregistered algorithm errors", "ghost-s", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ok, err := crypto.Verify(c.alg, []byte("pub"), []byte("message"), []byte("sig"))
		//: the failure arm distinguishes "scheme missing" via a sentinel, ok=false.
		if c.wantErr {
			if ok || !errs.HasCode(err, crypto.CodeUnknownSignatureAlgorithm) {
				t.Errorf("%s: Verify=(%v,%v) want (false,UnknownSignatureAlgorithm)", c.name, ok, err)
			}
			return
		}
		//: a registered scheme reports validity with no error channel.
		if err != nil || ok != c.wantOK {
			t.Errorf("%s: Verify=(%v,%v) want (%v,nil)", c.name, ok, err, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
