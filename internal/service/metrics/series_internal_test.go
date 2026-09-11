// Package metrics — series identity.
package metrics

import (
	"slices"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// label is a terse constructor for the tables below.
func label(key, value string) coremetrics.LabelValue {
	return coremetrics.LabelValue{Key: key, Value: value}
}

// Test_sortLabels pins that a label set is a SET.
//
// Without the sort, {a,b} and {b,a} encode to two different keys, so one
// metric's total splits across two series that nothing downstream can
// recombine — and the cardinality bound is spent twice on one identity.
func Test_sortLabels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []coremetrics.LabelValue
		want []coremetrics.LabelValue
	}
	tests := []tc{
		{name: "no labels is the dimensionless series", in: nil, want: nil},
		{name: "an empty slice is also dimensionless", in: []coremetrics.LabelValue{}, want: nil},
		{
			name: "one label is already ordered",
			in:   []coremetrics.LabelValue{label("a", "1")},
			want: []coremetrics.LabelValue{label("a", "1")},
		},
		{
			name: "an unordered pair is ordered by key",
			in:   []coremetrics.LabelValue{label("b", "2"), label("a", "1")},
			want: []coremetrics.LabelValue{label("a", "1"), label("b", "2")},
		},
		{
			//: exactly the stack scratch — one more and it goes to the heap,
			//: which must produce the same answer.
			name: "a full stack scratch",
			in:   reversedLabels(maxStackLabels),
			want: ascendingLabels(maxStackLabels),
		},
		{
			name: "beyond the stack scratch",
			in:   reversedLabels(maxStackLabels + 3),
			want: ascendingLabels(maxStackLabels + 3),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf [maxStackLabels]coremetrics.LabelValue
		got := sortLabels(buf[:0], c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("sortLabels = %v, want %v", got, c.want)
		}
		//: the caller's slice is never reordered in place — a caller who
		//: reuses one label slice across metrics would otherwise watch it
		//: change under them.
		if len(c.in) > 1 && slices.Equal(c.in, got) && !slices.IsSortedFunc(c.in, compareByKey) {
			t.Error("sortLabels reordered the caller's own slice")
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
// (name, labels) pairs never produce the same key.
//
// A label value is data — a route, a tenant id, a header. With a delimiter
// encoding, a caller who can influence one value can forge another series' key
// and have two unrelated series accumulate into one, which is a correctness
// hole that reads as a mysterious metric. Length prefixes remove it.
func Test_appendSeriesKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		metric string
		labels []coremetrics.LabelValue
	}
	tests := []tc{
		{name: "a bare name", metric: "requests"},
		{name: "a name that looks like an encoded key", metric: "\x08requests"},
		{
			name: "one label", metric: "requests",
			labels: []coremetrics.LabelValue{label("method", "GET")},
		},
		{
			//: the adversarial pair: a value carrying what a delimiter-based
			//: encoding would read as a separator plus another label.
			name: "a value containing separators", metric: "requests",
			labels: []coremetrics.LabelValue{label("a", "x\x00b\x00y")},
		},
		{
			name: "the two labels that value tried to forge", metric: "requests",
			labels: []coremetrics.LabelValue{label("a", "x"), label("b", "y")},
		},
		{
			name: "an empty value", metric: "requests",
			labels: []coremetrics.LabelValue{label("a", "")},
		},
		{
			name: "a name absorbing its label", metric: "requestsa",
			labels: []coremetrics.LabelValue{label("", "")},
		},
	}
	//: every case must hash to its own key; a collision is the failure.
	seen := make(map[string]string, len(tests))
	for _, c := range tests {
		key := string(appendSeriesKey(nil, c.metric, c.labels))
		if other, clash := seen[key]; clash {
			t.Errorf("%q and %q encode to the same series key %q", c.name, other, key)
		}
		seen[key] = c.name
	}

	//: and the encoding is a pure function of its inputs.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			first := string(appendSeriesKey(nil, c.metric, c.labels))
			second := string(appendSeriesKey(nil, c.metric, c.labels))
			if first != second {
				t.Errorf("two encodings of one series differ: %q vs %q", first, second)
			}
			//: appending onto a non-empty buffer extends it, never replaces it.
			grown := string(appendSeriesKey([]byte("prefix"), c.metric, c.labels))
			if grown != "prefix"+first {
				t.Errorf("appendSeriesKey did not append onto the buffer: %q", grown)
			}
		})
	}
}

// Test_validateLabels pins the refusal of a label set that cannot name a
// series. Both cases are structure, not data: a key is written at the call
// site, so it is wrong on the first call or never.
func Test_validateLabels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		labels    []coremetrics.LabelValue
		wantPanic bool
	}
	tests := []tc{
		{name: "no labels", labels: nil},
		{name: "one ordinary label", labels: []coremetrics.LabelValue{label("a", "1")}},
		{
			name:   "two distinct keys",
			labels: []coremetrics.LabelValue{label("a", "1"), label("b", "2")},
		},
		{
			//: an empty VALUE is a legitimate reading.
			name:   "an empty value",
			labels: []coremetrics.LabelValue{label("a", "")},
		},
		{
			name:      "an empty key",
			labels:    []coremetrics.LabelValue{label("", "1")},
			wantPanic: true,
		},
		{
			name:      "the same key twice",
			labels:    []coremetrics.LabelValue{label("a", "1"), label("a", "2")},
			wantPanic: true,
		},
		{
			//: even with identical values — the set is still not a set.
			name:      "the same key and value twice",
			labels:    []coremetrics.LabelValue{label("a", "1"), label("a", "1")},
			wantPanic: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if !c.wantPanic {
				if r != nil {
					t.Errorf("validateLabels panicked: %v", r)
				}
				return
			}
			if r == nil {
				t.Fatal("an unusable label set did not panic")
			}
			//: the panic names the typed sentinel, so the message points at
			//: the contract rather than at an encoding accident downstream.
			msg, isString := r.(string)
			if !isString || msg != coremetrics.InvalidLabel.Error() {
				t.Errorf("the panic value is %v, want the InvalidLabel message", r)
			}
		}()
		//: validateLabels runs on the SORTED set, which is where duplicates
		//: become adjacent.
		var buf [maxStackLabels]coremetrics.LabelValue
		validateLabels(sortLabels(buf[:0], c.labels))
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_compareLabels pins the total order Collect uses to make a snapshot
// deterministic. Without a total order two collections of the same meter
// render differently, and nothing downstream is diffable.
func Test_compareLabels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b []coremetrics.LabelValue
		want int
	}
	tests := []tc{
		{name: "two dimensionless sets are equal", want: 0},
		{
			name: "the dimensionless set sorts first",
			b:    []coremetrics.LabelValue{label("a", "1")},
			want: -1,
		},
		{
			name: "keys decide first",
			a:    []coremetrics.LabelValue{label("a", "9")},
			b:    []coremetrics.LabelValue{label("b", "1")},
			want: -1,
		},
		{
			name: "equal keys fall through to values",
			a:    []coremetrics.LabelValue{label("a", "1")},
			b:    []coremetrics.LabelValue{label("a", "2")},
			want: -1,
		},
		{
			name: "identical sets are equal",
			a:    []coremetrics.LabelValue{label("a", "1")},
			b:    []coremetrics.LabelValue{label("a", "1")},
			want: 0,
		},
		{
			name: "a common prefix is broken by length",
			a:    []coremetrics.LabelValue{label("a", "1")},
			b:    []coremetrics.LabelValue{label("a", "1"), label("b", "2")},
			want: -1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := compareLabels(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("compareLabels = %d, want sign %d", got, c.want)
		}
		//: antisymmetry — otherwise a sort's result depends on input order.
		if back := compareLabels(c.b, c.a); sign(back) != -c.want {
			t.Errorf("compareLabels reversed = %d, want sign %d", back, -c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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

// ascendingLabels builds n labels with keys already in ascending order.
func ascendingLabels(n int) []coremetrics.LabelValue {
	out := make([]coremetrics.LabelValue, n)
	for i := range out {
		out[i] = label("k"+string(rune('a'+i)), "v"+string(rune('a'+i)))
	}
	return out
}

// reversedLabels builds the same n labels in descending key order.
func reversedLabels(n int) []coremetrics.LabelValue {
	out := ascendingLabels(n)
	slices.Reverse(out)
	return out
}
