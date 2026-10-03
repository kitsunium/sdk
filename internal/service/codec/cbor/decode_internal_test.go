package cbor

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_decodeState_corrupt hands the decode walk input validation would
// have refused, as a defect in the validator would. Every read is checked:
// the walk fails with UNMARSHAL_FAILED and never panics.
func Test_decodeState_corrupt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		data   string
		target func() reflect.Value
	}
	untyped := func() reflect.Value { return reflect.New(reflect.TypeFor[any]()).Elem() }
	tests := []tc{
		{"empty", "", untyped},
		{"cut head", "19", untyped},
		{"cut payload", "4301", untyped},
		{"cut chunk", "5f4301", untyped},
		{"unclosed chunks", "5f", untyped},
		{"cut array", "8201", untyped},
		{"unclosed indefinite array", "9f01", untyped},
		{"cut map", "a201", untyped},
		{"cut struct", "a1616e", func() reflect.Value { return reflect.New(reflect.TypeFor[struct{ N int }]()).Elem() }},
		{"cut slice", "830102", func() reflect.Value { return reflect.New(reflect.TypeFor[[]int]()).Elem() }},
		{"cut skipped value", "a1617a83", func() reflect.Value { return reflect.New(reflect.TypeFor[struct{ N int }]()).Elem() }},
		{"cut bignum", "c2", func() reflect.Value { return reflect.New(reflect.TypeFor[int]()).Elem() }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := hex.DecodeString(tc.data)
		if err != nil {
			t.Fatalf("%s: bad hex fixture: %v", tc.name, err)
		}
		d := decodeState{data: data}
		target := tc.target()
		plan := decodePlanFor(target.Type())
		err = plan.kind.decode(&d, target, plan)
		if !errs.HasReason(err, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(err), "validation should have refused") {
			t.Errorf("%s: err = %v (%s), want the corrupt-input refusal", tc.name, err, errs.PrivateOf(err))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_hashable covers the map key check of a key type with an interface
// inside, at every depth it can hide.
func Test_hashable(t *testing.T) {
	t.Parallel()
	type withIface struct{ K any }
	type tc struct {
		name string
		v    any
		want bool
	}
	tests := []tc{
		{"scalar", 1, true},
		{"nil interface", [1]any{}, true},
		{"slice in an interface", [1]any{[]int{1}}, false},
		{"map in a struct", withIface{K: map[string]int{}}, false},
		{"string in a struct", withIface{K: "a"}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := hashable(reflect.ValueOf(tc.v)); got != tc.want {
			t.Errorf("%s: hashable = %v, want %v", tc.name, got, tc.want)
		}
		if !mayHoldUnhashable(reflect.TypeOf(tc.v)) && !tc.want {
			t.Errorf("%s: the type was not flagged for checking", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
