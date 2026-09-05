package id_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubGen is a comparable in-test Generator for registry behaviour checks.
type stubGen struct{ name id.Scheme }

func (s stubGen) Scheme() id.Scheme  { return s.name }
func (stubGen) New() (string, error) { return "stub", nil }

// TestRegisterLookupAvailable covers register → lookup → available + Known.
func TestRegisterLookupAvailable(t *testing.T) {
	got := id.Register(stubGen{name: "stub-a"})
	//: Register hands the generator back for singleton binding.
	if got.Scheme() != "stub-a" {
		t.Fatalf("Register returned scheme %q, want stub-a", got.Scheme())
	}
	//: the freshly-registered scheme resolves and reports Known.
	if _, ok := id.Lookup("stub-a"); !ok {
		t.Error("Lookup(stub-a) missed after Register")
	}
	if !id.Scheme("stub-a").Known() {
		t.Error("Scheme(stub-a).Known() = false after Register")
	}
	//: the empty scheme is the reserved invalid zero value.
	if id.Scheme("").Known() {
		t.Error("empty Scheme reported Known")
	}
}

// TestNewUnknownScheme surfaces UnknownScheme for an unregistered scheme.
func TestNewUnknownScheme(t *testing.T) {
	t.Parallel()
	_, err := id.New("does-not-exist")
	//: a missing scheme must surface the typed UNKNOWN_SCHEME sentinel.
	if !errs.HasCode(err, id.CodeUnknownScheme) {
		t.Fatalf("New(unknown) err=%v, want CodeUnknownScheme", err)
	}
}

// TestRegisterDuplicatePanics confirms a distinct generator on a taken scheme panics.
func TestRegisterDuplicatePanics(t *testing.T) {
	id.Register(stubGen{name: "dup-scheme"})
	defer func() {
		//: a distinct generator on a taken scheme must panic at boot.
		if recover() == nil {
			t.Error("duplicate distinct registration did not panic")
		}
	}()
	//: a DIFFERENT concrete value under the same scheme is the hard conflict.
	id.Register(stubGen2{name: "dup-scheme"})
}

// TestRegisterEmptySchemePanics pins the reserved-zero-value contract:
// Scheme("") is documented invalid and Scheme.Known() hard-codes false for it,
// so the registry must refuse to publish under it. Registering an empty scheme
// would otherwise leave Lookup/New resolving a key that Known() reports as
// absent — two accessors disagreeing about the same scheme.
func TestRegisterEmptySchemePanics(t *testing.T) {
	defer func() {
		//: Register turns the registry error into a boot-time panic.
		if recover() == nil {
			t.Error("Register(Scheme(\"\")) did not panic")
		}
	}()
	//: the empty scheme must never reach the registry map.
	id.Register(stubGen{name: ""})
}

// TestEmptySchemeStaysUnresolvable confirms the state after a refused
// registration: neither Lookup nor Known may report the empty scheme, and New
// must still answer UnknownScheme rather than dispatching to a ghost entry.
func TestEmptySchemeStaysUnresolvable(t *testing.T) {
	t.Parallel()
	//: the refused registration left no entry behind.
	if _, ok := id.Lookup(""); ok {
		t.Error("Lookup(\"\") resolved — the empty scheme reached the registry")
	}
	//: Known stays false, matching its documented hard-coded contract.
	if id.Scheme("").Known() {
		t.Error("Scheme(\"\").Known() = true")
	}
	//: dispatch surfaces the typed sentinel, not a ghost generator.
	if _, err := id.New(""); !errs.HasCode(err, id.CodeUnknownScheme) {
		t.Errorf("New(\"\") err=%v, want CodeUnknownScheme", err)
	}
}

// stubGen2 is a distinct comparable type so the dup check sees a conflict.
type stubGen2 struct{ name id.Scheme }

func (s stubGen2) Scheme() id.Scheme  { return s.name }
func (stubGen2) New() (string, error) { return "stub2", nil }
