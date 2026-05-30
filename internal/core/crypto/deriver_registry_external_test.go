package crypto_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeDeriver is a comparable Deriver whose Derive returns length zero-bytes, so
// the registry/dispatch tests need no real key-derivation function.
type fakeDeriver struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the Deriver port.
var _ crypto.Deriver = (*fakeDeriver)(nil)

func (f fakeDeriver) Algorithm() crypto.Algorithm { return f.name }

func (fakeDeriver) Derive(_, _ []byte, _ string, length int) (subkey []byte, err error) {
	return make([]byte, length), nil
}

func TestRegisterDeriver(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakeDeriver
	}
	tests := []tc{
		{"first deriver registers", fakeDeriver{name: "fd-1"}},
		{"second slot, distinct deriver", fakeDeriver{name: "fd-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterDeriver(c.arg)
		//: RegisterDeriver hands back the deriver and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterDeriver returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupDeriver(c.arg.name); !ok {
			t.Errorf("LookupDeriver(%q) failed after RegisterDeriver", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide deriver registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterDeriverPanics(t *testing.T) {
	//: clean slate so the conflict panics on its OWN second registration.
	crypto.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil deriver panics", func() { crypto.RegisterDeriver(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				crypto.RegisterDeriver(fakeDeriver{name: "dup-d"})
				//: a DISTINCT deriver under the same name is the hard conflict.
				crypto.RegisterDeriver(distinctDeriver{})
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means RegisterDeriver failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", c.name)
				}
			}()
			c.run()
		})
	}
}

// distinctDeriver is a SECOND Deriver type claiming the same Name as a
// fakeDeriver, to exercise the distinct-duplicate conflict (a different type).
type distinctDeriver struct{}

func (distinctDeriver) Algorithm() crypto.Algorithm { return "dup-d" }

func (distinctDeriver) Derive(_, _ []byte, _ string, _ int) (subkey []byte, err error) {
	return nil, nil
}

func TestLookupDeriver(t *testing.T) {
	//: sequential — seeds + reads the process-wide deriver registry.
	crypto.ResetForTest()
	crypto.RegisterDeriver(fakeDeriver{name: "lk-d"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered deriver resolves", "lk-d", true},
		{"unregistered misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupDeriver reports presence by Algorithm.
		if _, ok := crypto.LookupDeriver(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupDeriver(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableDerivers(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []crypto.Algorithm
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two derivers listed sorted ascending", []crypto.Algorithm{"av-d-b", "av-d-a"}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.ResetForTest()
		//: seed the registry with the row's derivers before listing.
		for _, name := range c.seed {
			crypto.RegisterDeriver(fakeDeriver{name: name})
		}
		got := crypto.AvailableDerivers()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: AvailableDerivers()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded deriver, sorted.
		if len(got) != c.wantLen || got[0] != crypto.Algorithm("av-d-a") || got[1] != crypto.Algorithm("av-d-b") {
			t.Errorf("%s: AvailableDerivers()=%v want [av-d-a av-d-b]", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSubkey(t *testing.T) {
	//: sequential — seeds + reads the process-wide deriver registry.
	crypto.ResetForTest()
	crypto.RegisterDeriver(fakeDeriver{name: "sub-d"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		length  int
		wantErr bool
	}
	tests := []tc{
		{"registered deriver yields the requested length", "sub-d", 32, false},
		{"unregistered algorithm errors", "ghost-d", 32, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := crypto.Subkey(c.alg, []byte("secret"), []byte("salt"), "info", c.length)
		//: the failure arm surfaces UnknownKDFAlgorithm.
		if c.wantErr {
			if got != nil || !errs.HasCode(err, crypto.CodeUnknownKDFAlgorithm) {
				t.Errorf("%s: Subkey=(%v,%v) want (nil,UnknownKDFAlgorithm)", c.name, got, err)
			}
			return
		}
		//: the success arm returns exactly the requested number of bytes.
		if err != nil || len(got) != c.length {
			t.Errorf("%s: Subkey=(%d bytes,%v) want (%d,nil)", c.name, len(got), err, c.length)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
