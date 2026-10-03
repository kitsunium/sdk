// Package crypto — white-box tests for the shared registrar. Every scheme
// registry (AEAD, Hasher, Signer, MAC, Deriver, Agreement, PasswordHasher,
// StreamSealer) refuses through it, so a bug in this file is a bug in all eight
// at once. The table under it is the kernel's and is tested there
// (internal/kernel/plugin): what is pinned here is the crypto domain's half —
// the three outcomes a registrar collapses into one panic, the code those
// panics carry, and the verb that names the registrar in them.
package crypto

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubScheme is a comparable value for white-box schemeRegistry tests. It
// implements no capability port — the registrar only needs a comparable value
// that names its Algorithm, so a bare struct exercises the publish paths.
type stubScheme struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubScheme) Algorithm() Algorithm { return s.id }

// : the uncomparable double must satisfy the port its registry stores, exactly
// : as the stub hasher does.
var _ Hasher = uncomparableHasher{}

// uncomparableHasher is a Hasher that holds a slice, so == on it panics — the
// shape the registrar must refuse before its duplicate check compares it.
type uncomparableHasher struct {
	stubHasher
	tags []string
}

// Test_schemeRegistry_publish proves the three outcomes every Register* verb
// collapses into one panic: a first publish under a free name succeeds, an
// idempotent re-publish of the SAME value is a no-op, and a DISTINCT value
// under a taken name is a hard conflict. Only the middle one is invisible from
// outside — a scheme package imported through two paths registers twice, and
// turning that into a boot panic would make the SDK unusable in a diamond
// dependency.
func Test_schemeRegistry_publish(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		first   stubScheme
		second  stubScheme
		publish int
		wantErr bool
	}
	tests := []tc{
		{"a free name", stubScheme{id: "free"}, stubScheme{}, 1, false},
		{"the same value twice", stubScheme{id: "same"}, stubScheme{id: "same"}, 2, false},
		{"a distinct value under a taken name", stubScheme{id: "taken"}, stubScheme{id: "taken", tag: 1}, 2, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a fresh local registry isolates each case from the package globals.
		r := schemeRegistry[stubScheme]{verb: "RegisterStub"}
		name := c.first.id

		if err := r.publish(name, c.first); err != nil {
			t.Fatalf("the first publish of %q = %v, want nil", name, err)
		}
		if c.publish > 1 {
			err := r.publish(name, c.second)
			if (err != nil) != c.wantErr {
				t.Fatalf("the second publish of %q = %v, want an error: %v", name, err, c.wantErr)
			}
		}

		//: whatever happened, the first registration still resolves — a refused
		//: publish may not disturb what was already there.
		got, ok := r.table.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missed after publishing", name)
		}
		if got != c.first {
			t.Errorf("Lookup(%q) = %v, want the first-published %v", name, got, c.first)
		}
		if names := r.table.Names(); !slices.Contains(names, name) {
			t.Errorf("Names() = %v, missing %q", names, name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_schemeRegistry_conflictIsTyped proves a refused publish is an SDK error
// with the duplicate-registration code (rule 2) — matchable with errs.HasCode
// rather than by parsing text — and that the panic text every registrar
// renders from it still names the registrar and the algorithm.
func Test_schemeRegistry_conflictIsTyped(t *testing.T) {
	t.Parallel()
	r := schemeRegistry[stubScheme]{verb: "RegisterStub"}
	if err := r.publish("typed", stubScheme{id: "typed"}); err != nil {
		t.Fatalf("the first publish = %v, want nil", err)
	}
	err := r.publish("typed", stubScheme{id: "typed", tag: 1})
	if !errs.HasCode(err, CodeDuplicateRegistration) || !errors.Is(err, DuplicateRegistration) {
		t.Fatalf("the conflict = %v, want the typed DuplicateRegistration", err)
	}
	text := conflictText(err)
	for _, want := range []string{"[0.2.4.1 DUPLICATE_REGISTRATION]", `registrar="crypto.RegisterStub"`, `algorithm="typed"`} {
		if !strings.Contains(text, want) {
			t.Errorf("conflictText = %q, want it to say %s", text, want)
		}
	}
}

// Test_schemeRegistry_register pins the registrar every public Register* is
// now one line of: it hands the value back for a singleton binding, refuses an
// unusable value BEFORE asking its Algorithm, and refuses a conflict — both
// with a panic naming the registrar through its verb and carrying the code.
// Eight registrars used to spell these two messages each; a verb typed into
// the wrong one is the copy-paste error this test exists to keep out.
func Test_schemeRegistry_register(t *testing.T) {
	t.Parallel()
	//: a registry of the Hasher port, as hashers is, under a verb of its own.
	r := schemeRegistry[Hasher]{verb: "RegisterStub"}
	kept := stubHasher{id: "kept"}
	if got := r.register(kept); got != Hasher(kept) {
		t.Fatalf("register returned %v, want the value it was given", got)
	}
	//: the identical value again is the idempotent no-op, not a panic.
	if got := r.register(kept); got != Hasher(kept) {
		t.Fatalf("re-register returned %v, want the value it was given", got)
	}
	tests := []struct {
		name  string
		value Hasher
		want  []string
	}{
		{"an untyped nil", nil, []string{"crypto.RegisterStub [0.2.4.1 DUPLICATE_REGISTRATION]: nil"}},
		{"a typed nil", (*stubHasher)(nil), []string{"crypto.RegisterStub [0.2.4.1 DUPLICATE_REGISTRATION]: nil *crypto.stubHasher"}},
		{"an uncomparable value", uncomparableHasher{tags: []string{"x"}}, []string{"crypto.uncomparableHasher is not comparable"}},
		{"a distinct value under a taken name", stubHasher{id: "kept", tag: 1}, []string{"[0.2.4.1 DUPLICATE_REGISTRATION]", `registrar="crypto.RegisterStub"`, `algorithm="kept"`}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			msg := recoverRegister(&r, c.value)
			for _, want := range c.want {
				if !strings.Contains(msg, want) {
					t.Errorf("register panicked with %q, want it to say %q", msg, want)
				}
			}
		})
	}
}

// recoverRegister runs r.register(v) and returns what it panicked with, or the
// empty string when it did not panic.
func recoverRegister(r *schemeRegistry[Hasher], v Hasher) (msg string) {
	defer func() {
		//: the registrar refuses by panicking; read the refusal back.
		if recovered := recover(); recovered != nil {
			msg = fmt.Sprint(recovered)
		}
	}()
	r.register(v)
	return ""
}
