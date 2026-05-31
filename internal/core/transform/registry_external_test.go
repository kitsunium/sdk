package transform_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/transform"
)

// fakeCompressor is a trivial, non-compressing Compressor used to exercise the
// registry + dispatch without a real codec. Methods take a pointer receiver so
// a *fakeCompressor is comparable, making re-registration of the SAME pointer
// the idempotent no-op the registry contract requires. Interface conformance is
// proven at compile time by every transform.Register(&fakeCompressor{…}) call
// site below (Register's parameter is transform.Compressor).
type fakeCompressor struct {
	name transform.Algorithm
}

func (f *fakeCompressor) Algorithm() transform.Algorithm { return f.name }

func (f *fakeCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: identity transform — the registry tests only care about routing.
	return append(dst, src...), nil
}

func (f *fakeCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: identity transform — mirrors Compress for round-trip routing tests.
	return append(dst, src...), nil
}

func TestRegister(t *testing.T) {
	//: clean slate so this test survives `go test -count=N`.
	transform.ResetForTest()
	type tc struct {
		name string
		arg  *fakeCompressor
	}
	tests := []tc{
		{"first scheme registers", &fakeCompressor{name: "reg-1"}},
		{"second slot, distinct scheme", &fakeCompressor{name: "reg-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		transform.Register(c.arg)
		//: the scheme must be resolvable immediately after Register.
		if _, ok := transform.Lookup(c.arg.name); !ok {
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
	transform.ResetForTest()
	transform.Register(&fakeCompressor{name: "lk-fake"})
	type tc struct {
		name   string
		in     transform.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered scheme resolves", "lk-fake", true},
		{"unregistered scheme misses", "absent-zzz", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Lookup reports presence without the caller naming a wire id.
		if _, ok := transform.Lookup(c.in); ok != c.wantOK {
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
	transform.ResetForTest()
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil scheme panics", func() { transform.Register(nil) }},
		{
			"duplicate Algorithm panics",
			func() {
				transform.Register(&fakeCompressor{name: "dup-name"})
				//: a DISTINCT scheme value (different pointer) under the same
				//: name is the hard conflict the registry must reject.
				transform.Register(&fakeCompressor{name: "dup-name"})
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			//: a missing panic means Register failed to guard the case.
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", c.name)
			}
		}()
		c.run()
	}
	for _, c := range tests {
		//: sequential — each row mutates the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterIdempotent(t *testing.T) {
	//: clean slate so the same-scheme re-registration is the only mutation.
	transform.ResetForTest()
	type tc struct {
		name string
		arg  *fakeCompressor
	}
	tests := []tc{{"re-registering the same scheme is a no-op", &fakeCompressor{name: "idem"}}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		transform.Register(c.arg)
		//: the SAME pointer re-registers without panicking (idempotent).
		got := transform.Register(c.arg)
		//: Register hands back the scheme it was given.
		if got.Algorithm() != c.arg.name {
			t.Errorf("idempotent Register returned %q want %q", got.Algorithm(), c.arg.name)
		}
		//: still resolvable after the idempotent re-register.
		if _, ok := transform.Lookup(c.arg.name); !ok {
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
		seed    []*fakeCompressor
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two schemes listed sorted ascending", []*fakeCompressor{{name: "av-b"}, {name: "av-a"}}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		transform.ResetForTest()
		//: seed the registry with the row's schemes before listing.
		for _, f := range c.seed {
			transform.Register(f)
		}
		got := transform.Available()
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
		if got[0] != transform.Algorithm("av-a") || got[1] != transform.Algorithm("av-b") {
			t.Errorf("%s: Available()=%v want [av-a av-b] sorted", c.name, got)
		}
	}
	for _, c := range tests {
		//: sequential — Available reads the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
