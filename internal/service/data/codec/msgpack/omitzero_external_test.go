package msgpack_test

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// alwaysZero says it is zero whatever its bits hold.
type alwaysZero struct {
	// N keeps reflect's zero test false when set.
	N int
}

// IsZero is the type's own notion of zero, which omitzero asks first.
func (alwaysZero) IsZero() bool { return true }

// negativeZero declares IsZero on its pointer only, so omitzero reaches it
// through the field's address — or an addressable copy.
type negativeZero struct {
	// N is zero, for IsZero, when negative.
	N int
}

// IsZero reports a negative N.
func (z *negativeZero) IsZero() bool { return z.N < 0 }

// omitZeroPair is #1103's acceptance shape: a time and an empty struct, both
// omitzero.
type omitZeroPair struct {
	T time.Time `yaml:"t,omitzero" msgpack:"t,omitzero" bson:"t,omitzero"`
	S struct{}  `yaml:"s,omitzero" msgpack:"s,omitzero" bson:"s,omitzero"`
}

// omitZeroMethods holds the IsZero shapes and the omitempty combination.
type omitZeroMethods struct {
	A alwaysZero   `yaml:"a,omitzero" msgpack:"a,omitzero" bson:"a,omitzero"`
	P negativeZero `yaml:"p,omitzero" msgpack:"p,omitzero" bson:"p,omitzero"`
	R *alwaysZero  `yaml:"r,omitzero" msgpack:"r,omitzero" bson:"r,omitzero"`
	E []int        `yaml:"e,omitempty,omitzero" msgpack:"e,omitempty,omitzero" bson:"e,omitempty,omitzero"`
	K []int        `yaml:"k,omitzero" msgpack:"k,omitzero" bson:"k,omitzero"`
}

// TestOmitZero is #1103: omitzero leaves a field out exactly when
// encoding/json does — the type's IsZero first, through the address when only
// the pointer declares it, reflect's zero value otherwise, and either of
// omitempty and omitzero enough to leave it out.
func TestOmitZero(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{"the zero pair is an empty document", omitZeroPair{}, nil},
		{"a set time is written", omitZeroPair{T: when}, []string{"t"}},
		{"IsZero wins over non-zero bits", omitZeroMethods{A: alwaysZero{N: 5}, P: negativeZero{N: 1}, K: []int{}}, []string{"k", "p"}},
		{"a pointer-receiver IsZero is asked through a copy", omitZeroMethods{P: negativeZero{N: -1}}, nil},
		{"a non-nil pointer asks its IsZero", omitZeroMethods{P: negativeZero{N: -1}, R: &alwaysZero{N: 1}}, nil},
		{"omitempty and omitzero combine", omitZeroMethods{P: negativeZero{N: -1}, E: []int{}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, value := range []any{tc.value, ptrTo(tc.value)} {
				data, err := msgpack.New().Marshal(value)
				if err != nil {
					t.Fatalf("Marshal %T: %v", value, err)
				}
				var back map[string]any
				if err := msgpack.New().Unmarshal(data, &back); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				if got := slices.Sorted(maps.Keys(back)); !slices.Equal(got, tc.want) {
					t.Fatalf("%T wrote keys %q, want %q", value, got, tc.want)
				}
			}
		})
	}
}

// ptrTo returns a pointer to a copy of v, so the fields are addressable.
func ptrTo(v any) any {
	switch x := v.(type) {
	case omitZeroPair:
		return &x
	case omitZeroMethods:
		return &x
	default:
		return v
	}
}
