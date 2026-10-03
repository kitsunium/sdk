package cbor

import (
	"reflect"
	"testing"
)

// layoutDeep embeds one struct twice at the same depth, which cancels it,
// and another at two depths, where the shallower wins.
type layoutDeep struct {
	layoutA
	layoutB
	Top int `cbor:"top"`
}

// layoutA declares "dup" and "shallow".
type layoutA struct {
	Dup     int `cbor:"dup"`
	Shallow int `cbor:"shallow"`
}

// layoutB declares "dup" too, at the same depth as layoutA's.
type layoutB struct {
	Dup int `cbor:"dup"`
	layoutC
}

// layoutC declares "shallow" one level deeper than layoutA does.
type layoutC struct {
	Shallow int `cbor:"shallow"`
}

// layoutTagged has an untagged and a tagged field fighting for one key.
type layoutTagged struct {
	layoutUntagged
	Name int `cbor:"Name"`
}

// layoutUntagged declares Name without a tag.
type layoutUntagged struct {
	Name int
}

// layoutKeys mixes text keys, canonicalised integer keys, options, and an
// empty tag, which names nothing and does not make its field dominant.
type layoutKeys struct {
	_     struct{} `cbor:"ignored,toarray"`
	One   int      `cbor:"+01,keyasint,omitempty"`
	Text  string   `json:"text,omitzero"`
	Plain bool     `cbor:""`
	skip  int
	Minus int `cbor:"-"`
}

// Test_layoutOf pins field resolution: embedding and dominance, tag
// precedence, option parsing, integer key canonicalisation and toarray.
func Test_layoutOf(t *testing.T) {
	t.Parallel()
	type field struct {
		name  string
		index []int
		flags fieldFlag
	}
	type tc struct {
		name    string
		typ     reflect.Type
		want    []field
		toArray bool
	}
	tests := []tc{
		{"same depth cancels, shallower wins", reflect.TypeFor[layoutDeep](), []field{
			{"shallow", []int{0, 1}, flagTagged},
			{"top", []int{2}, flagTagged},
		}, false},
		{"tagged dominates untagged at the same depth", reflect.TypeFor[layoutTagged](), []field{
			{"Name", []int{1}, flagTagged},
		}, false},
		{"keys and options", reflect.TypeFor[layoutKeys](), []field{
			{"1", []int{1}, flagTagged | flagKeyAsInt | flagOmitEmpty},
			{"text", []int{2}, flagTagged | flagOmitZero},
			{"Plain", []int{3}, 0},
		}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		layout := layoutOf(tc.typ)
		if layout.toArray != tc.toArray || layout.refusal != "" {
			t.Fatalf("%s: toArray %v refusal %q", tc.name, layout.toArray, layout.refusal)
		}
		if len(layout.fields) != len(tc.want) {
			t.Fatalf("%s: %d fields, want %d", tc.name, len(layout.fields), len(tc.want))
		}
		for i, f := range layout.fields {
			w := tc.want[i]
			if f.name != w.name || !reflect.DeepEqual(f.index, w.index) || f.flags != w.flags {
				t.Errorf("%s: field %d = %q %v %b, want %q %v %b", tc.name, i, f.name, f.index, f.flags, w.name, w.index, w.flags)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_canonicalIntKeys parses keyasint names and refuses one that is not
// an integer.
func Test_canonicalIntKeys(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          string
		wantName    string
		wantInt     int64
		wantRefusal bool
	}
	tests := []tc{
		{"plain", "7", "7", 7, false},
		{"leading zero and sign", "+007", "7", 7, false},
		{"negative", "-3", "-3", -3, false},
		{"not an integer", "x", "x", 0, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		fields, refusal := canonicalIntKeys([]*structField{{name: tc.in, flags: flagKeyAsInt}})
		if (refusal != "") != tc.wantRefusal {
			t.Fatalf("%s: refusal %q, want refused %v", tc.name, refusal, tc.wantRefusal)
		}
		if fields[0].name != tc.wantName || fields[0].nameInt != tc.wantInt {
			t.Errorf("%s: got %q / %d, want %q / %d", tc.name, fields[0].name, fields[0].nameInt, tc.wantName, tc.wantInt)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
