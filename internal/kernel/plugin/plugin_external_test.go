package plugin_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// port is the shape a registry would publish: a small interface every fixture
// below satisfies, so what Unusable judges is the VALUE and never the type set.
type port interface{ Name() string }

// pointerPlug is the ordinary shape — a pointer receiver, so a nil of its type
// satisfies port while being unable to serve one call.
type pointerPlug struct{ name string }

func (p *pointerPlug) Name() string { return p.name }

// valuePlug is a comparable value plug-in: the shape that must pass.
type valuePlug struct{ name string }

func (v valuePlug) Name() string { return v.name }

// slicePlug carries a slice, so its type is not comparable and == on it is a
// runtime panic — the registry's duplicate check is exactly that comparison.
type slicePlug struct {
	name string
	tags []string
}

func (s slicePlug) Name() string { return s.name }

// funcPlug is a func-typed plug-in: never comparable, and it is the shape that
// makes "not comparable" a real case rather than a theoretical one.
type funcPlug func() string

func (f funcPlug) Name() string { return f() }

// TestUnusableNamesTheTwoShapesARegistryCannotStore covers both refusals and
// the two acceptances, through an interface — because the whole point is that
// the compiler has already said yes.
//
// Both refusals were observed on the real registries before this package
// existed: registering (*nilable)(nil) through transform.Register stored it and
// Lookup returned "(*transform_test.nilable)(nil)", while a second
// non-comparable plug-in under a taken name panicked with "runtime error:
// comparing uncomparable type transform_test.uncomparable" instead of the
// domain's DUPLICATE_REGISTRATION.
func TestUnusableNamesTheTwoShapesARegistryCannotStore(t *testing.T) {
	t.Parallel()
	var nilPointer *pointerPlug
	var nilFunc funcPlug
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"untyped nil says so without a type", nil, "nil"},
		{"a typed nil pointer names its type", port(nilPointer), "nil *plugin_test.pointerPlug"},
		{"a nil func plug-in is caught before comparability", port(nilFunc), "nil plugin_test.funcPlug"},
		{"a slice field makes the type uncomparable", port(slicePlug{name: "s"}), "plugin_test.slicePlug is not comparable"},
		{"a non-nil func is still uncomparable", port(funcPlug(func() string { return "f" })), "plugin_test.funcPlug is not comparable"},
		{"a comparable value passes", port(valuePlug{name: "v"}), ""},
		{"a live pointer passes", port(&pointerPlug{name: "p"}), ""},
	}
	runCase := func(t *testing.T, v any, want string) {
		t.Helper()
		if got := plugin.Unusable(v); got != want {
			t.Errorf("Unusable() = %q, want %q", got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc.v, tc.want)
		})
	}
}

// TestAPassingValueSurvivesTheComparisonARegistryWillMake is the other half of
// the contract: Unusable returning "" is a promise that == on the value does
// not panic, which is the operation the caller is about to perform. A guard
// that admitted an uncomparable value would move the panic one line down and
// into the registry, where the message no longer names the plug-in.
func TestAPassingValueSurvivesTheComparisonARegistryWillMake(t *testing.T) {
	t.Parallel()
	passing := []port{valuePlug{name: "v"}, &pointerPlug{name: "p"}}
	for _, p := range passing {
		if why := plugin.Unusable(p); why != "" {
			t.Fatalf("fixture %T refused: %s", p, why)
		}
		//: the registry's own duplicate check, performed here on purpose.
		if p != p { //nolint:staticcheck //: the comparison IS the assertion.
			t.Errorf("%T compared unequal to itself", p)
		}
	}
}
