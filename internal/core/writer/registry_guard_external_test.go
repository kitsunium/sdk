package writer_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// bracketed is rule 4's log-parser header. A refusal must still carry one:
// widening the guard must not cost the dotted-quad code an operator greps for.
var bracketed = regexp.MustCompile(`\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`)

// typedNilPlug's nil pointer satisfies the port — the shape the `== nil` guard
// let through, after which every lookup returned a value that panics on use.
// The port is embedded rather than implemented: the refusal happens BEFORE any
// method is called, which is the property under test.
type typedNilPlug struct{ writer.Factory }

// uncomparablePlug holds a slice, so `==` on it is a runtime panic — and `==`
// is exactly what the registry's duplicate check performs.
type uncomparablePlug struct {
	writer.Factory
	tags []string
}

// TestRegisterRefusesAPlugInTheRegistryCannotStore pins both refusals on the
// real registrar. Observed before the guard, on transform.Register: a typed nil
// was stored and Lookup returned "(*transform_test.nilable)(nil)", while a
// second non-comparable plug-in under a taken name panicked with "runtime
// error: comparing uncomparable type transform_test.uncomparable" — Go's
// comparison, not the domain's conflict.
func TestRegisterRefusesAPlugInTheRegistryCannotStore(t *testing.T) {
	t.Parallel()
	var typedNil *typedNilPlug
	tests := []struct {
		name     string
		register func()
		want     string
	}{
		{"a typed nil factory", func() { writer.Register(typedNil) }, "nil *writer_test.typedNilPlug"},
		{"a non-comparable factory", func() { writer.Register(uncomparablePlug{tags: []string{"x"}}) }, "writer_test.uncomparablePlug is not comparable"},
	}
	runCase := func(t *testing.T, name string, register func(), want string) {
		t.Helper()
		defer func() {
			//: the registrars refuse by panicking at import time, so recover is
			//: the only place the refusal can be read.
			r := recover()
			if r == nil {
				t.Fatalf("%s: registered without a refusal", name)
			}
			msg, isString := r.(string)
			if !isString {
				t.Fatalf("%s: panicked with %T (%v), want the registrar's message", name, r, r)
			}
			if !strings.Contains(msg, want) {
				t.Errorf("%s: refusal %q does not say %q", name, msg, want)
			}
			if !bracketed.MatchString(msg) {
				t.Errorf("%s: refusal %q carries no dotted-quad header", name, msg)
			}
		}()
		register()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc.name, tc.register, tc.want)
		})
	}
}
