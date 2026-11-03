package codec_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// refusal is a decode refusal carrying fields, as a codec builds one.
func refusal(fields ...errs.FieldValue) error {
	return errs.Wrap(nil, errs.WrapParams{
		Code:    0x00_03_02_02,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "decoding failed",
		Private: "position test",
	}, fields...)
}

// keys returns the keys of fields, in order.
func keys(fields []errs.FieldValue) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Key()+"="+f.StringValue())
	}
	return out
}

// TestPositionFieldsCarriesWhatIsKnown pins which coordinates a position
// writes: a binary position its offset even at the first byte, a text
// position its line and column and an offset only when not zero.
func TestPositionFieldsCarriesWhatIsKnown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    codec.PositionValue
		want []string
	}{
		{"first byte", codec.PositionValue{}, []string{"offset=0"}},
		{"binary", codec.PositionValue{Offset: 118}, []string{"offset=118"}},
		{"text", codec.PositionValue{Line: 12, Column: 4}, []string{"line=12", "column=4"}},
		{"line only", codec.PositionValue{Line: 3}, []string{"line=3"}},
		{"all three", codec.PositionValue{Offset: 40, Line: 3, Column: 7}, []string{"offset=40", "line=3", "column=7"}},
	}
	for _, tc := range cases {
		got := keys(codec.PositionFields(tc.p))
		if len(got) != len(tc.want) {
			t.Errorf("%s: PositionFields = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: PositionFields = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestPositionOfRoundTrips reads back what PositionFields wrote, through a
// wrap that adds fields of its own.
func TestPositionOfRoundTrips(t *testing.T) {
	t.Parallel()
	for _, p := range []codec.PositionValue{{}, {Offset: 118}, {Line: 12, Column: 4}, {Offset: 40, Line: 3, Column: 7}} {
		err := errs.Wrap(refusal(codec.PositionFields(p)...), errs.WrapParams{}, errs.String("detail", "outer"))
		got, ok := codec.PositionOf(err)
		if !ok || got != p {
			t.Errorf("PositionOf(fields of %+v) = %+v, %v; want it back, true", p, got, ok)
		}
	}
}

// TestPositionOfWithoutPosition is false for an error carrying no
// coordinate, for a stdlib error and for nil.
func TestPositionOfWithoutPosition(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"no field":     refusal(errs.String("detail", "x")),
		"stdlib":       errors.New("plain"),
		"nil":          nil,
		"named column": refusal(errs.String(codec.FieldColumn, "price")),
	} {
		if p, ok := codec.PositionOf(err); ok {
			t.Errorf("%s: PositionOf = %+v, true; want false", name, p)
		}
	}
}

// TestPositionOfKeepsTheOrigin keeps the first value of a key along the
// chain: the origin's, not a wrapper's.
func TestPositionOfKeepsTheOrigin(t *testing.T) {
	t.Parallel()
	inner := refusal(codec.PositionFields(codec.PositionValue{Offset: 7})...)
	outer := errs.Wrap(inner, errs.WrapParams{}, codec.PositionFields(codec.PositionValue{Offset: 99})...)
	if p, ok := codec.PositionOf(outer); !ok || p.Offset != 7 {
		t.Errorf("PositionOf = %+v, %v; want the origin's offset 7", p, ok)
	}
}
