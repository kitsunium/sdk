package crypto_test

import (
	"crypto/sha256"
	"hash"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeHasher is a comparable Hasher whose New returns SHA-256 (32-byte, stable),
// so the registry/dispatch tests need no real per-algorithm scheme.
type fakeHasher struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the Hasher port.
var _ crypto.Hasher = (*fakeHasher)(nil)

func (f fakeHasher) Algorithm() crypto.Algorithm { return f.name }

func (fakeHasher) New() hash.Hash { return sha256.New() }

func TestRegisterHasher(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakeHasher
	}
	tests := []tc{
		{"first hasher registers", fakeHasher{name: "fh-1"}},
		{"second slot, distinct hasher", fakeHasher{name: "fh-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterHasher(c.arg)
		//: RegisterHasher hands back the hasher and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterHasher returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupHasher(c.arg.name); !ok {
			t.Errorf("LookupHasher(%q) failed after RegisterHasher", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide hasher registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterHasherPanics(t *testing.T) {
	//: clean slate so the conflict panics on its OWN second registration.
	crypto.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil hasher panics", func() { crypto.RegisterHasher(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				crypto.RegisterHasher(fakeHasher{name: "dup-h"})
				//: a DISTINCT hasher under the same name is the hard conflict.
				crypto.RegisterHasher(distinctHasher{})
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means RegisterHasher failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", c.name)
				}
			}()
			c.run()
		})
	}
}

// distinctHasher is a SECOND Hasher type claiming the same Name as a fakeHasher,
// to exercise the distinct-duplicate conflict (a different dynamic type).
type distinctHasher struct{}

func (distinctHasher) Algorithm() crypto.Algorithm { return "dup-h" }

func (distinctHasher) New() hash.Hash { return sha256.New() }

func TestLookupHasher(t *testing.T) {
	//: sequential — seeds + reads the process-wide hasher registry.
	crypto.ResetForTest()
	crypto.RegisterHasher(fakeHasher{name: "lk-h"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered hasher resolves", "lk-h", true},
		{"unregistered misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupHasher reports presence by Algorithm.
		if _, ok := crypto.LookupHasher(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupHasher(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableHashers(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []crypto.Algorithm
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two hashers listed sorted ascending", []crypto.Algorithm{"av-h-b", "av-h-a"}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.ResetForTest()
		//: seed the registry with the row's hashers before listing.
		for _, name := range c.seed {
			crypto.RegisterHasher(fakeHasher{name: name})
		}
		got := crypto.AvailableHashers()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: AvailableHashers()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded hasher, sorted.
		if len(got) != c.wantLen || got[0] != crypto.Algorithm("av-h-a") || got[1] != crypto.Algorithm("av-h-b") {
			t.Errorf("%s: AvailableHashers()=%v want [av-h-a av-h-b]", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSum(t *testing.T) {
	//: sequential — seeds + reads the process-wide hasher registry.
	crypto.ResetForTest()
	crypto.RegisterHasher(fakeHasher{name: "sum-h"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered hasher sums to 32 bytes", "sum-h", false},
		{"unregistered algorithm errors", "ghost-h", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := crypto.Sum(c.alg, []byte("payload"))
		//: the failure arm surfaces UnknownHashAlgorithm.
		if c.wantErr {
			if !errs.HasCode(err, crypto.CodeUnknownHashAlgorithm) {
				t.Errorf("%s: err=%v want UnknownHashAlgorithm", c.name, err)
			}
			return
		}
		//: SHA-256 digest is 32 bytes; the fake uses it.
		if err != nil || len(got) != sha256.Size {
			t.Errorf("%s: Sum=(%d bytes,%v) want (32,nil)", c.name, len(got), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSumHex(t *testing.T) {
	//: sequential — seeds + reads the process-wide hasher registry.
	crypto.ResetForTest()
	crypto.RegisterHasher(fakeHasher{name: "hex-h"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantLen int
	}
	tests := []tc{
		{"registered hasher hex is 64 chars", "hex-h", sha256.Size * 2},
		{"unregistered algorithm errors", "ghost-h", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := crypto.SumHex(c.alg, []byte("payload"))
		//: a zero wantLen marks the unknown-algorithm error arm.
		if c.wantLen == 0 {
			if !errs.HasCode(err, crypto.CodeUnknownHashAlgorithm) {
				t.Errorf("%s: err=%v want UnknownHashAlgorithm", c.name, err)
			}
			return
		}
		//: lowercase hex of a 32-byte digest is 64 characters.
		if err != nil || len(got) != c.wantLen {
			t.Errorf("%s: SumHex len=%d (%v) want %d", c.name, len(got), err, c.wantLen)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestNewHash(t *testing.T) {
	//: sequential — seeds + reads the process-wide hasher registry.
	crypto.ResetForTest()
	crypto.RegisterHasher(fakeHasher{name: "new-h"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered hasher yields a streaming hash", "new-h", false},
		{"unregistered algorithm errors", "ghost-h", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h, err := crypto.NewHash(c.alg)
		//: the failure arm surfaces UnknownHashAlgorithm + a nil hash.
		if c.wantErr {
			if h != nil || !errs.HasCode(err, crypto.CodeUnknownHashAlgorithm) {
				t.Errorf("%s: NewHash=(%v,%v) want (nil,UnknownHashAlgorithm)", c.name, h, err)
			}
			return
		}
		//: the streaming hash must be usable with the SHA-256 block size.
		if err != nil || h == nil || h.Size() != sha256.Size {
			t.Errorf("%s: NewHash=(%v,%v)", c.name, h, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
