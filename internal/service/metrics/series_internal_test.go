// Package metrics — series identity.
package metrics

import (
	"math"
	"slices"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// attr is a terse constructor for the string cases in the tables below.
func attr(key, value string) coremetrics.AttrValue {
	return coremetrics.String(key, value)
}

// Test_sortAttrs pins that an attribute set is a SET.
//
// Without the sort, {a,b} and {b,a} encode to two different keys, so one
// metric's total splits across two series that nothing downstream can
// recombine — and the cardinality bound is spent twice on one identity.
func Test_sortAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []coremetrics.AttrValue
		want []coremetrics.AttrValue
	}
	tests := []tc{
		{name: "no attributes is the dimensionless series", in: nil, want: nil},
		{name: "an empty slice is also dimensionless", in: []coremetrics.AttrValue{}, want: nil},
		{
			name: "one attribute is already ordered",
			in:   []coremetrics.AttrValue{attr("a", "1")},
			want: []coremetrics.AttrValue{attr("a", "1")},
		},
		{
			name: "an unordered pair is ordered by key",
			in:   []coremetrics.AttrValue{attr("b", "2"), attr("a", "1")},
			want: []coremetrics.AttrValue{attr("a", "1"), attr("b", "2")},
		},
		{
			//: the four kinds sort by key alone — a value's type never moves
			//: it in the set.
			name: "mixed kinds order by key",
			in: []coremetrics.AttrValue{
				coremetrics.Float64("d", 1.5),
				coremetrics.Bool("b", true),
				coremetrics.Int64("c", 7),
				attr("a", "x"),
			},
			want: []coremetrics.AttrValue{
				attr("a", "x"),
				coremetrics.Bool("b", true),
				coremetrics.Int64("c", 7),
				coremetrics.Float64("d", 1.5),
			},
		},
		{
			//: exactly the stack scratch — one more and it goes to the heap,
			//: which must produce the same answer.
			name: "a full stack scratch",
			in:   reversedAttrs(maxStackAttrs),
			want: ascendingAttrs(maxStackAttrs),
		},
		{
			name: "beyond the stack scratch",
			in:   reversedAttrs(maxStackAttrs + 3),
			want: ascendingAttrs(maxStackAttrs + 3),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf [maxStackAttrs]coremetrics.AttrValue
		got := sortAttrs(buf[:0], c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("sortAttrs = %v, want %v", got, c.want)
		}
		//: the caller's slice is never reordered in place — a caller who
		//: reuses one attribute slice across metrics would otherwise watch it
		//: change under them.
		if len(c.in) > 1 && slices.Equal(c.in, got) && !slices.IsSortedFunc(c.in, coremetrics.CompareAttrKey) {
			t.Error("sortAttrs reordered the caller's own slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_appendSeriesKey pins that the encoding is INJECTIVE: two different
// (name, attrs) pairs never produce the same key.
//
// An attribute value is data — a route, a tenant id, a header. With a delimiter
// encoding, a caller who can influence one value can forge another series' key
// and have two unrelated series accumulate into one, which is a correctness
// hole that reads as a mysterious metric. Length prefixes remove it, and the
// KIND TAG extends the same property across types: with typed attributes,
// String("v", "1") and Int64("v", 1) would otherwise be indistinguishable the
// moment either was rendered.
func Test_appendSeriesKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		metric string
		attrs  []coremetrics.AttrValue
	}
	tests := []tc{
		{name: "a bare name", metric: "requests"},
		{name: "a name that looks like an encoded key", metric: "\x08requests"},
		{
			name: "one attribute", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("method", "GET")},
		},
		{
			//: the adversarial pair: a value carrying what a delimiter-based
			//: encoding would read as a separator plus another attribute.
			name: "a value containing separators", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("a", "x\x00b\x00y")},
		},
		{
			name: "the two attributes that value tried to forge", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("a", "x"), attr("b", "y")},
		},
		{
			name: "an empty value", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("a", "")},
		},
		{
			name: "a name absorbing its attribute", metric: "requestsa",
			attrs: []coremetrics.AttrValue{attr("", "")},
		},
		{
			//: the typed-attribute adversary: four kinds that all render as
			//: "1" or "true" somewhere downstream must stay four keys.
			name: "the string one", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("v", "1")},
		},
		{
			name: "the integer one", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Int64("v", 1)},
		},
		{
			name: "the double one", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Float64("v", 1)},
		},
		{
			name: "the boolean one", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Bool("v", true)},
		},
		{
			name: "the string spelling of the boolean", metric: "requests",
			attrs: []coremetrics.AttrValue{attr("v", "true")},
		},
		{
			//: signed zero is two bit patterns and therefore two series.
			name: "positive zero", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Float64("v", 0)},
		},
		{
			name: "negative zero", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Float64("v", math.Copysign(0, -1))},
		},
	}
	//: every case must hash to its own key; a collision is the failure.
	seen := make(map[string]string, len(tests))
	for _, c := range tests {
		key := string(appendSeriesKey(nil, kindCounter, c.metric, c.attrs))
		if other, clash := seen[key]; clash {
			t.Errorf("%q and %q encode to the same series key %q", c.name, other, key)
		}
		seen[key] = c.name
	}

	//: and the encoding is a pure function of its inputs.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			first := string(appendSeriesKey(nil, kindCounter, c.metric, c.attrs))
			second := string(appendSeriesKey(nil, kindCounter, c.metric, c.attrs))
			if first != second {
				t.Errorf("two encodings of one series differ: %q vs %q", first, second)
			}
			//: appending onto a non-empty buffer extends it, never replaces it.
			grown := string(appendSeriesKey([]byte("prefix"), kindCounter, c.metric, c.attrs))
			if grown != "prefix"+first {
				t.Errorf("appendSeriesKey did not append onto the buffer: %q", grown)
			}
		})
	}
}

// TestValidateAttrs pins the refusal of an attribute set that cannot name a
// series. All three cases are structure, not data: a key and a KIND are both
// written at the call site, so they are wrong on the first call or never.
func TestValidateAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		attrs     []coremetrics.AttrValue
		wantPanic bool
	}
	tests := []tc{
		{name: "no attributes", attrs: nil},
		{name: "one ordinary attribute", attrs: []coremetrics.AttrValue{attr("a", "1")}},
		{
			name:  "two distinct keys",
			attrs: []coremetrics.AttrValue{attr("a", "1"), coremetrics.Int64("b", 2)},
		},
		{
			//: an empty VALUE is a legitimate reading.
			name:  "an empty value",
			attrs: []coremetrics.AttrValue{attr("a", "")},
		},
		{
			//: so is a false bool and a zero integer — a zero VALUE is data.
			name:  "zero-valued attributes of every kind",
			attrs: []coremetrics.AttrValue{coremetrics.Bool("a", false), coremetrics.Int64("b", 0), coremetrics.Float64("c", 0)},
		},
		{
			name:      "an empty key",
			attrs:     []coremetrics.AttrValue{attr("", "1")},
			wantPanic: true,
		},
		{
			//: a struct literal sets the Key and leaves the value unset; the
			//: kind is then AttrKindInvalid, which is not "the empty string".
			name:      "a value no constructor set",
			attrs:     []coremetrics.AttrValue{{Key: "a"}},
			wantPanic: true,
		},
		{
			name:      "the same key twice",
			attrs:     []coremetrics.AttrValue{attr("a", "1"), attr("a", "2")},
			wantPanic: true,
		},
		{
			//: even with identical values — the set is still not a set.
			name:      "the same key and value twice",
			attrs:     []coremetrics.AttrValue{attr("a", "1"), attr("a", "1")},
			wantPanic: true,
		},
		{
			//: and even when only the KIND differs, which is exactly the pair
			//: the identity encoding keeps apart.
			name:      "the same key with two different kinds",
			attrs:     []coremetrics.AttrValue{attr("a", "1"), coremetrics.Int64("a", 1)},
			wantPanic: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if !c.wantPanic {
				if r != nil {
					t.Errorf("ValidateAttrs panicked: %v", r)
				}
				return
			}
			if r == nil {
				t.Fatal("an unusable attribute set did not panic")
			}
			//: the panic names the typed sentinel, so the message points at
			//: the contract rather than at an encoding accident downstream.
			msg, isString := r.(string)
			if !isString || msg != coremetrics.InvalidAttribute.Error() {
				t.Errorf("the panic value is %v, want the InvalidAttribute message", r)
			}
		}()
		//: ValidateAttrs runs on the SORTED set, which is where duplicates
		//: become adjacent.
		var buf [maxStackAttrs]coremetrics.AttrValue
		coremetrics.ValidateAttrs(sortAttrs(buf[:0], c.attrs))
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_compareAttrs pins the total order Collect uses to make a snapshot
// deterministic. Without a total order two collections of the same meter
// render differently, and nothing downstream is diffable.
func Test_compareAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b []coremetrics.AttrValue
		want int
	}
	tests := []tc{
		{name: "two dimensionless sets are equal", want: 0},
		{
			name: "the dimensionless set sorts first",
			b:    []coremetrics.AttrValue{attr("a", "1")},
			want: -1,
		},
		{
			name: "keys decide first",
			a:    []coremetrics.AttrValue{attr("a", "9")},
			b:    []coremetrics.AttrValue{attr("b", "1")},
			want: -1,
		},
		{
			name: "equal keys fall through to values",
			a:    []coremetrics.AttrValue{attr("a", "1")},
			b:    []coremetrics.AttrValue{attr("a", "2")},
			want: -1,
		},
		{
			name: "identical sets are equal",
			a:    []coremetrics.AttrValue{attr("a", "1")},
			b:    []coremetrics.AttrValue{attr("a", "1")},
			want: 0,
		},
		{
			//: the two the wire would render identically stay ordered, which
			//: is what keeps the sort from calling two distinct series equal.
			name: "a string and an integer that spell the same",
			a:    []coremetrics.AttrValue{attr("a", "1")},
			b:    []coremetrics.AttrValue{coremetrics.Int64("a", 1)},
			want: -1,
		},
		{
			name: "integers order numerically, not as bits",
			a:    []coremetrics.AttrValue{coremetrics.Int64("a", -1)},
			b:    []coremetrics.AttrValue{coremetrics.Int64("a", 1)},
			want: -1,
		},
		{
			name: "false precedes true",
			a:    []coremetrics.AttrValue{coremetrics.Bool("a", false)},
			b:    []coremetrics.AttrValue{coremetrics.Bool("a", true)},
			want: -1,
		},
		{
			name: "doubles order numerically",
			a:    []coremetrics.AttrValue{coremetrics.Float64("a", -2)},
			b:    []coremetrics.AttrValue{coremetrics.Float64("a", 0.5)},
			want: -1,
		},
		{
			//: two distinct series must never compare equal, or their order
			//: falls back to map iteration and the snapshot stops being
			//: byte-identical twice in a row.
			//: the direction is the bit-pattern order, which is arbitrary but
			//: TOTAL — that is the only property a deterministic sort needs.
			name: "signed zeros are two distinct series",
			a:    []coremetrics.AttrValue{coremetrics.Float64("a", 0)},
			b:    []coremetrics.AttrValue{coremetrics.Float64("a", math.Copysign(0, -1))},
			want: -1,
		},
		{
			name: "a common prefix is broken by length",
			a:    []coremetrics.AttrValue{attr("a", "1")},
			b:    []coremetrics.AttrValue{attr("a", "1"), attr("b", "2")},
			want: -1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := compareAttrs(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("compareAttrs = %d, want sign %d", got, c.want)
		}
		//: antisymmetry — otherwise a sort's result depends on input order.
		if back := compareAttrs(c.b, c.a); sign(back) != -c.want {
			t.Errorf("compareAttrs reversed = %d, want sign %d", back, -c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCompareAttrValueSeparatesEveryNaNPayload pins the one comparison a
// numeric ordering alone gets wrong: NaN != NaN, so cmp.Compare reports two
// NaNs equal, and two series the map keeps apart would sort equal and swap
// places between collections.
func TestCompareAttrValueSeparatesEveryNaNPayload(t *testing.T) {
	t.Parallel()
	quiet := coremetrics.Float64("a", math.NaN())
	loud := coremetrics.Float64("a", math.Float64frombits(math.Float64bits(math.NaN())|2))
	if coremetrics.CompareAttrValue(quiet, loud) == 0 {
		t.Error("two NaN payloads compare equal, so two distinct series would sort equal")
	}
	if coremetrics.CompareAttrValue(quiet, quiet) != 0 {
		t.Error("a NaN does not compare equal to itself, so the order is not reflexive")
	}
}

// sign normalises a comparator result to -1, 0 or 1.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// ascendingAttrs builds n attributes with keys already in ascending order.
func ascendingAttrs(n int) []coremetrics.AttrValue {
	out := make([]coremetrics.AttrValue, n)
	for i := range out {
		out[i] = attr("k"+string(rune('a'+i)), "v"+string(rune('a'+i)))
	}
	return out
}

// reversedAttrs builds the same n attributes in descending key order.
func reversedAttrs(n int) []coremetrics.AttrValue {
	out := ascendingAttrs(n)
	slices.Reverse(out)
	return out
}
