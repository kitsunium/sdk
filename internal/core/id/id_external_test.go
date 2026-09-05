// Package id_test — black-box tests for the scheme dispatch surface.
package id_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : both stubs must satisfy Generator at compile time, exactly as a real scheme
// : package does when it binds its singleton.
var (
	_ id.Generator = (*stubGen)(nil)
	_ id.Generator = (*stubGen2)(nil)
)

// stubGen is a comparable in-test Generator for registry behaviour checks.
type stubGen struct{ name id.Scheme }

// Scheme names the stub so the registry can key on it.
func (s stubGen) Scheme() id.Scheme { return s.name }

// New returns a fixed value; the identifier itself is not what these tests pin.
func (stubGen) New() (string, error) { return "stub", nil }

// stubGen2 is a distinct comparable type so the dup check sees a conflict.
type stubGen2 struct{ name id.Scheme }

// Scheme names the second stub.
func (s stubGen2) Scheme() id.Scheme { return s.name }

// New returns a different fixed value from stubGen.
func (stubGen2) New() (string, error) { return "stub2", nil }

// TestNew surfaces the typed sentinel for anything the registry does not
// resolve. Dispatching to a zero Generator instead would panic far from the
// call that actually named the wrong scheme.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		scheme id.Scheme
	}
	tests := []tc{
		{"a scheme nobody registered", "does-not-exist"},
		//: the empty scheme is the reserved invalid zero value; the registry
		//: refuses to publish under it, so dispatch must miss too.
		{"the reserved empty scheme", ""},
		{"a scheme differing only in case", "STUB-A"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := id.New(c.scheme)
		if !errs.HasCode(err, id.CodeUnknownScheme) {
			t.Fatalf("New(%q) = %v, want CodeUnknownScheme", c.scheme, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Scheme_Known pins the reserved zero value. Known() hard-codes false for
// the empty scheme, and the registry refuses to publish under it, so the two
// can never disagree about whether it exists.
func Test_Scheme_Known(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		scheme id.Scheme
		want   bool
	}
	tests := []tc{
		{"the reserved empty scheme", "", false},
		{"a scheme nobody registered", "does-not-exist", false},
		{"a whitespace scheme", " ", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.scheme.Known(); got != c.want {
			t.Errorf("Scheme(%q).Known() = %v, want %v", c.scheme, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Scheme_String pins that a Scheme renders as itself, so a log line names
// the scheme the caller asked for rather than a decorated form of it.
func Test_Scheme_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		scheme id.Scheme
	}
	tests := []tc{
		{"an ordinary scheme", "uuidv4"},
		{"the empty scheme", ""},
		{"a scheme with a dash", "uuid-v7"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.scheme.String(); got != string(c.scheme) {
			t.Errorf("Scheme(%q).String() = %q, want %q", string(c.scheme), got, string(c.scheme))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
