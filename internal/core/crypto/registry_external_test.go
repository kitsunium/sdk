package crypto_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
)

// fakeAEAD is a trivial, non-cryptographic AEAD used to exercise the registry +
// dispatch without a real cipher. Its "box" is [Version][id][plaintext]; Open
// reverses it. It is comparable (value type) so idempotent re-registration of
// the same value is a no-op, matching the contract.
type fakeAEAD struct {
	name crypto.Algorithm
	id   byte
}

// : compile-time proof the fake satisfies the AEAD port.
var _ crypto.AEAD = (*fakeAEAD)(nil)

func (f fakeAEAD) Algorithm() crypto.Algorithm { return f.name }

func (f fakeAEAD) ID() byte { return f.id }

func (f fakeAEAD) Seal(_ crypto.Key, plaintext, _ []byte) ([]byte, error) {
	//: trivial framing — the dispatch tests only care about header routing.
	box := []byte{crypto.Version, f.id}
	return append(box, plaintext...), nil
}

func (f fakeAEAD) Open(_ crypto.Key, box, _ []byte) ([]byte, error) {
	//: reject anything that is not our own well-formed frame (non-oracle).
	if len(box) < 2 || box[0] != crypto.Version || box[1] != f.id {
		return nil, crypto.DecryptionFailed
	}
	return box[2:], nil
}

func TestRegister(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakeAEAD
	}
	tests := []tc{
		{"first scheme registers", fakeAEAD{name: "reg-1", id: 0x01}},
		{"second slot, distinct scheme", fakeAEAD{name: "reg-2", id: 0x02}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.Register(c.arg)
		//: the scheme must be resolvable immediately after Register.
		if _, ok := crypto.Lookup(c.arg.name); !ok {
			t.Errorf("Lookup(%q) failed after Register", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — Register mutates the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestLookup(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	crypto.ResetForTest()
	crypto.Register(fakeAEAD{name: "lk-fake", id: 0x05})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered scheme resolves", "lk-fake", true},
		{"unregistered scheme misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Lookup reports presence without the caller naming the wire id.
		if _, ok := crypto.Lookup(c.in); ok != c.wantOK {
			t.Errorf("%s: Lookup(%q) ok=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		//: sequential — Lookup reads the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterPanics(t *testing.T) {
	//: clean slate so each conflict panics on its OWN second registration.
	crypto.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil scheme panics", func() { crypto.Register(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				crypto.Register(fakeAEAD{name: "dup-name", id: 0x10})
				//: a DISTINCT scheme (different id) under the same name conflicts.
				crypto.Register(fakeAEAD{name: "dup-name", id: 0x11})
			},
		},
		{
			"duplicate wire id panics",
			func() {
				crypto.Register(fakeAEAD{name: "id-a", id: 0x20})
				//: a DISTINCT scheme under a taken wire id conflicts.
				crypto.Register(fakeAEAD{name: "id-b", id: 0x20})
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means Register failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", c.name)
				}
			}()
			c.run()
		})
	}
}

func TestRegisterIdempotent(t *testing.T) {
	//: clean slate so the same-scheme re-registration is the only mutation.
	crypto.ResetForTest()
	type tc struct {
		name string
		arg  fakeAEAD
	}
	tests := []tc{{"re-registering the same scheme is a no-op", fakeAEAD{name: "idem", id: 0x30}}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.Register(c.arg)
		//: the SAME scheme value re-registers without panicking (idempotent).
		got := crypto.Register(c.arg)
		//: Register hands back the scheme it was given.
		if got.Algorithm() != c.arg.name {
			t.Errorf("idempotent Register returned %q want %q", got.Algorithm(), c.arg.name)
		}
		//: still resolvable after the idempotent re-register.
		if _, ok := crypto.Lookup(c.arg.name); !ok {
			t.Errorf("Lookup(%q) failed after idempotent re-register", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — Register mutates the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailable(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []fakeAEAD
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two schemes listed sorted ascending", []fakeAEAD{{name: "av-b", id: 0x41}, {name: "av-a", id: 0x42}}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.ResetForTest()
		//: seed the registry with the row's schemes before listing.
		for _, f := range c.seed {
			crypto.Register(f)
		}
		got := crypto.Available()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: Available()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded scheme, sorted.
		if len(got) != c.wantLen {
			t.Fatalf("%s: len=%d want %d (%v)", c.name, len(got), c.wantLen, got)
		}
		if got[0] != crypto.Algorithm("av-a") || got[1] != crypto.Algorithm("av-b") {
			t.Errorf("%s: Available()=%v want [av-a av-b] sorted", c.name, got)
		}
	}
	for _, c := range tests {
		//: sequential — Available reads the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
