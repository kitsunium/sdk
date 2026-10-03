package yaml_test

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/yaml"
)

// typedCase is one document decoded into one typed target.
type typedCase struct {
	// want is the target's value after a successful decode.
	want any
	// target returns a fresh pointer to decode into.
	target func() any
	// name names the case.
	name string
	// doc is the document.
	doc string
	// reason is the refusal's reason, or "" for a success.
	reason string
}

// runTypedCases decodes each case into its target and checks the outcome.
func runTypedCases(t *testing.T, tests []typedCase) {
	t.Helper()
	runCase := func(t *testing.T, tc typedCase) {
		t.Helper()
		target := tc.target()
		err := yaml.New().Unmarshal([]byte(tc.doc), target)
		//: a refusal.
		if tc.reason != "" {
			if !errs.HasReason(err, tc.reason) {
				t.Errorf("%s: Unmarshal(%q) error = %v, want %s", tc.name, tc.doc, err, tc.reason)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Unmarshal(%q) error = %v", tc.name, tc.doc, err)
		}
		if got := reflect.ValueOf(target).Elem().Interface(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Unmarshal(%q) = %#v, want %#v", tc.name, tc.doc, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// into returns a target constructor for T.
func into[T any]() func() any {
	//: a fresh *T per call.
	return func() any { return new(T) }
}

// TestScalarTargets decodes scalars into every scalar kind: what the core
// schema allows, the ranges, and the refusals.
func TestScalarTargets(t *testing.T) {
	t.Parallel()
	runTypedCases(t, []typedCase{
		//: a string takes any scalar's text.
		{name: "number into string", doc: "8080", target: into[string](), want: "8080"},
		{name: "float into string keeps its text", doc: "1.0", target: into[string](), want: "1.0"},
		{name: "boolean into string", doc: "true", target: into[string](), want: "true"},
		{name: "null leaves a string", doc: "~", target: into[string](), want: ""},
		//: booleans.
		{name: "true", doc: "true", target: into[bool](), want: true},
		{name: "YAML 1.1 yes refused", doc: "yes", target: into[bool](), reason: "UNMARSHAL_FAILED"},
		{name: "quoted true refused", doc: `"true"`, target: into[bool](), reason: "UNMARSHAL_FAILED"},
		//: integers.
		{name: "int", doc: "-42", target: into[int](), want: -42},
		{name: "hex into uint8", doc: "0xff", target: into[uint8](), want: uint8(255)},
		{name: "octal into int", doc: "0o755", target: into[int](), want: 493},
		{name: "int8 overflow", doc: "128", target: into[int8](), reason: "UNMARSHAL_FAILED"},
		{name: "negative into uint", doc: "-1", target: into[uint](), reason: "UNMARSHAL_FAILED"},
		{name: "uint64 max", doc: "18446744073709551615", target: into[uint64](), want: maxUint64},
		{name: "past uint64", doc: "18446744073709551616", target: into[uint64](), reason: "UNMARSHAL_FAILED"},
		{name: "whole float into int", doc: "1e3", target: into[int](), want: 1000},
		{name: "fraction into int", doc: "1.5", target: into[int](), reason: "UNMARSHAL_FAILED"},
		{name: "leading zero into int", doc: "0644", target: into[int](), reason: "LEADING_ZERO_REFUSED"},
		{name: "underscores into int", doc: "1_000", target: into[int](), reason: "UNMARSHAL_FAILED"},
		{name: "quoted number into int", doc: `"12"`, target: into[int](), reason: "UNMARSHAL_FAILED"},
		//: floats.
		{name: "float64", doc: "2.5", target: into[float64](), want: 2.5},
		{name: "int into float", doc: "3", target: into[float64](), want: 3.0},
		{name: "float32 rounds once", doc: "3.1415927", target: into[float32](), want: float32(3.1415927)},
		{name: "float32 overflow", doc: "1e39", target: into[float32](), reason: "UNMARSHAL_FAILED"},
		{name: "infinity into float32", doc: "-.inf", target: into[float32](), want: float32(math.Inf(-1))},
		//: time.
		{name: "duration", doc: "1h30m", target: into[time.Duration](), want: 90 * time.Minute},
		{name: "bare number duration", doc: "30", target: into[time.Duration](), reason: "UNMARSHAL_FAILED"},
		{name: "RFC 3339 time", doc: "2024-01-15T09:30:00Z", target: into[time.Time](), want: time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC)},
		{name: "quoted time", doc: `"2024-01-15T09:30:00Z"`, target: into[time.Time](), want: time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC)},
		{name: "date", doc: "2024-01-15", target: into[time.Time](), want: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)},
		{name: "not a time", doc: "soon", target: into[time.Time](), reason: "UNMARSHAL_FAILED"},
		//: kinds YAML has nothing for.
		{name: "complex", doc: "1", target: into[complex128](), reason: "UNMARSHAL_FAILED"},
		{name: "mapping into int", doc: "{a: 1}", target: into[int](), reason: "UNMARSHAL_FAILED"},
	})
}

// TestCollectionTargets decodes collections into maps, slices, arrays,
// pointers and interfaces.
func TestCollectionTargets(t *testing.T) {
	t.Parallel()
	one := 1
	runTypedCases(t, []typedCase{
		{name: "map of ints", doc: "a: 1\nb: 2\n", target: into[map[string]int](), want: map[string]int{"a": 1, "b": 2}},
		{name: "int keys", doc: "1: a\n0x2: b\n", target: into[map[int]string](), want: map[int]string{1: "a", 2: "b"}},
		{name: "int keys colliding", doc: "1: a\n0x1: b\n", target: into[map[int]string](), reason: "DUPLICATE_KEY"},
		{name: "null key into an int key", doc: "~: a\n", target: into[map[int]string](), reason: "UNMARSHAL_FAILED"},
		{name: "slice", doc: "[1, 2, 3]", target: into[[]int](), want: []int{1, 2, 3}},
		{name: "array", doc: "[1, 2]", target: into[[2]int](), want: [2]int{1, 2}},
		{name: "array too short", doc: "[1]", target: into[[2]int](), reason: "UNMARSHAL_FAILED"},
		{name: "pointer allocated", doc: "1", target: into[*int](), want: &one},
		{name: "null into pointer", doc: "null", target: func() any { p := &one; return &p }, want: (*int)(nil)},
		{name: "any", doc: "[1, a]", target: into[any](), want: []any{1, "a"}},
		{name: "sequence into a map", doc: "[1]", target: into[map[string]int](), reason: "UNMARSHAL_FAILED"},
		{name: "scalar into a slice", doc: "x", target: into[[]int](), reason: "UNMARSHAL_FAILED"},
		{name: "scalar into bytes", doc: "AQI=", target: into[[]byte](), reason: "UNMARSHAL_FAILED"},
		{name: "bytes from integers", doc: "[1, 2]", target: into[[]byte](), want: []byte{1, 2}},
		{name: "non-empty interface", doc: "x", target: into[fmt.Stringer](), reason: "UNMARSHAL_FAILED"},
	})
}

// TestStructDecoding checks a struct ignores unknown keys, keeps fields the
// document does not set, and decodes into the inline map.
func TestStructDecoding(t *testing.T) {
	t.Parallel()
	type config struct {
		Name  string `yaml:"name"`
		Port  int    `yaml:"port"`
		Debug bool   `yaml:"debug"`
	}
	runTypedCases(t, []typedCase{
		{name: "unknown key ignored", doc: "name: x\nunknown: 1\n", target: into[config](), want: config{Name: "x"}},
		{name: "fields kept", doc: "port: 80\n", target: func() any { return &config{Name: "kept", Port: 1} }, want: config{Name: "kept", Port: 80}},
		{name: "mismatch named", doc: "port: eighty\n", target: into[config](), reason: "UNMARSHAL_FAILED"},
		{name: "case-sensitive keys", doc: "Name: x\n", target: into[config](), want: config{}},
	})
}

// TestMapsMerge decodes into a map that already holds keys, as yaml.v3 did:
// the document's keys are added, the others kept.
func TestMapsMerge(t *testing.T) {
	t.Parallel()
	runTypedCases(t, []typedCase{
		{name: "merged", doc: "b: 2\n", target: func() any { return &map[string]any{"a": 1} }, want: map[string]any{"a": 1, "b": 2}},
		{name: "null empties", doc: "~", target: func() any { return &map[string]any{"a": 1} }, want: map[string]any(nil)},
	})
}

// TestTargetMustBeANonNilPointer refuses a target the decoder cannot write
// through.
func TestTargetMustBeANonNilPointer(t *testing.T) {
	t.Parallel()
	type tc struct {
		target any
		name   string
	}
	var nilMap *map[string]any
	tests := []tc{{name: "value", target: map[string]any{}}, {name: "nil pointer", target: nilMap}, {name: "nil", target: nil}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if err := yaml.New().Unmarshal([]byte("a: 1"), tc.target); !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: Unmarshal error = %v, want UNMARSHAL_FAILED", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
