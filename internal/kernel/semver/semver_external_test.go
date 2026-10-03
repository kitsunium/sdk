package semver_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// reading is one input and what the package must say about it. canonical is
// x/mod's canonical form of the input — "" for an invalid one — kept as DATA:
// two inputs compare equal exactly when their canonical forms are the same
// string, which is how the pairwise test below reads the table.
type reading struct {
	in         string
	canonical  string
	prerelease string
}

// vectors are the inputs golang.org/x/mod/semver pins in its own suite
// (semver_test.go, `tests`), with x/mod's canonical form for each and in the
// same order — which is ASCENDING PRECEDENCE, every invalid string first —
// and the pre-release written out rather than derived. They are what makes
// this package a drop-in replacement for the one it retires: the two agree on
// every row, for every function the two have in common.
var vectors = []reading{
	{in: "bad"},
	{in: "v1-alpha.beta.gamma"},
	{in: "v1-pre"},
	{in: "v1+meta"},
	{in: "v1-pre+meta"},
	{in: "v1.2-pre"},
	{in: "v1.2+meta"},
	{in: "v1.2-pre+meta"},
	{"v1.0.0-alpha", "v1.0.0-alpha", "-alpha"},
	{"v1.0.0-alpha.1", "v1.0.0-alpha.1", "-alpha.1"},
	{"v1.0.0-alpha.beta", "v1.0.0-alpha.beta", "-alpha.beta"},
	{"v1.0.0-beta", "v1.0.0-beta", "-beta"},
	{"v1.0.0-beta.2", "v1.0.0-beta.2", "-beta.2"},
	{"v1.0.0-beta.11", "v1.0.0-beta.11", "-beta.11"},
	{"v1.0.0-rc.1", "v1.0.0-rc.1", "-rc.1"},
	{"v1", "v1.0.0", ""},
	{"v1.0", "v1.0.0", ""},
	{"v1.0.0", "v1.0.0", ""},
	{"v1.2", "v1.2.0", ""},
	{"v1.2.0", "v1.2.0", ""},
	{"v1.2.3-456", "v1.2.3-456", "-456"},
	{"v1.2.3-456.789", "v1.2.3-456.789", "-456.789"},
	{"v1.2.3-456-789", "v1.2.3-456-789", "-456-789"},
	{"v1.2.3-456a", "v1.2.3-456a", "-456a"},
	{"v1.2.3-pre", "v1.2.3-pre", "-pre"},
	{"v1.2.3-pre+meta", "v1.2.3-pre", "-pre"},
	{"v1.2.3-pre.1", "v1.2.3-pre.1", "-pre.1"},
	{"v1.2.3-zzz", "v1.2.3-zzz", "-zzz"},
	{"v1.2.3", "v1.2.3", ""},
	{"v1.2.3+meta", "v1.2.3", ""},
	{"v1.2.3+meta-pre", "v1.2.3", ""},
	{"v1.2.3+meta-pre.sha.256a", "v1.2.3", ""},
}

// TestReadings pins both readings of every vector: its validity, and its
// pre-release — "" for a release and for an invalid string alike.
func TestReadings(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c reading) {
		t.Helper()
		valid := c.canonical != ""
		if got := semver.IsValid(c.in); got != valid {
			t.Errorf("IsValid(%q) = %v, want %v", c.in, got, valid)
		}
		if got := semver.Prerelease(c.in); got != c.prerelease {
			t.Errorf("Prerelease(%q) = %q, want %q", c.in, got, c.prerelease)
		}
	}
	for _, c := range vectors {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCompareOrdersTheVectorsAsListed compares every vector with every other:
// equal when their canonical forms are the same string, and otherwise ordered
// as the table lists them, which is precedence order. x/mod's suite makes the
// same 1 024 comparisons over the same table.
func TestCompareOrdersTheVectorsAsListed(t *testing.T) {
	t.Parallel()
	for i, left := range vectors {
		for j, right := range vectors {
			want := 0
			//: different canonical forms are ordered by their place in the table.
			if left.canonical != right.canonical {
				want = sign(i - j)
			}
			if got := semver.Compare(left.in, right.in); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", left.in, right.in, got, want)
			}
		}
	}
}

// TestSortingWithCompareGivesPrecedenceOrder sorts a shuffled copy of the
// vectors with Compare — the way a caller sorts, there being no Sort in this
// package — breaking ties by string, and expects exactly x/mod's golden order
// for its own Sort, which breaks ties the same way.
func TestSortingWithCompareGivesPrecedenceOrder(t *testing.T) {
	t.Parallel()
	list := make([]string, 0, len(vectors))
	//: reversed, so the sort has real work to do.
	for _, v := range slices.Backward(vectors) {
		list = append(list, v.in)
	}
	slices.SortFunc(list, func(a, b string) int {
		//: precedence first, then the string, as x/mod's Sort does.
		if order := semver.Compare(a, b); order != 0 {
			return order
		}
		return strings.Compare(a, b)
	})
	golden := []string{
		"bad", "v1+meta", "v1-alpha.beta.gamma", "v1-pre", "v1-pre+meta",
		"v1.2+meta", "v1.2-pre", "v1.2-pre+meta",
		"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta",
		"v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1",
		"v1", "v1.0", "v1.0.0", "v1.2", "v1.2.0",
		"v1.2.3-456", "v1.2.3-456.789", "v1.2.3-456-789", "v1.2.3-456a",
		"v1.2.3-pre", "v1.2.3-pre+meta", "v1.2.3-pre.1", "v1.2.3-zzz",
		"v1.2.3", "v1.2.3+meta", "v1.2.3+meta-pre", "v1.2.3+meta-pre.sha.256a",
	}
	if !slices.Equal(list, golden) {
		t.Errorf("sorted =\n  %q\nwant\n  %q", list, golden)
	}
}

// TestSpecificationPrecedenceExamples walks the two chains SemVer 2.0.0 §11
// itself gives as examples, each element strictly below the next.
func TestSpecificationPrecedenceExamples(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		chain []string
	}
	tests := []tc{
		{name: "§11.2 numbers", chain: []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.1.1"}},
		{name: "§11.3 a release above its pre-releases", chain: []string{"v1.0.0-alpha", "v1.0.0"}},
		{name: "§11.4 pre-release identifiers", chain: []string{
			"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta",
			"v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0",
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for i := range len(c.chain) - 1 {
			low, high := c.chain[i], c.chain[i+1]
			if semver.Compare(low, high) != -1 || semver.Compare(high, low) != 1 {
				t.Errorf("Compare(%q, %q) = %d and back %d, want -1 and 1",
					low, high, semver.Compare(low, high), semver.Compare(high, low))
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValidity pins the edges of the grammar the vectors do not reach: the
// "v", leading zeros — refused on a number and on a numeric pre-release
// identifier, accepted in build metadata — empty identifiers, characters
// outside [0-9A-Za-z-], and trailing text.
func TestValidity(t *testing.T) {
	t.Parallel()
	type tc struct {
		in    string
		valid bool
	}
	tests := []tc{
		{in: "", valid: false},
		{in: "v", valid: false},
		{in: "1.0.0", valid: false},
		{in: "V1.0.0", valid: false},
		{in: " v1.0.0", valid: false},
		{in: "v1.0.0 ", valid: false},
		{in: "v0", valid: true},
		{in: "v0.0.0", valid: true},
		{in: "v01", valid: false},
		{in: "v1.02", valid: false},
		{in: "v1.0.03", valid: false},
		{in: "v1.", valid: false},
		{in: "v1..0", valid: false},
		{in: "v1.0.", valid: false},
		{in: "v1.0.0.0", valid: false},
		{in: "v1.0.0-", valid: false},
		{in: "v1.0.0+", valid: false},
		{in: "v1.0.0-+meta", valid: false},
		{in: "v1.0.0-a..b", valid: false},
		{in: "v1.0.0-a.", valid: false},
		{in: "v1.0.0-.a", valid: false},
		{in: "v1.0.0+a..b", valid: false},
		{in: "v1.0.0+a.", valid: false},
		{in: "v1.0.0-0", valid: true},
		{in: "v1.0.0-00", valid: false},
		{in: "v1.0.0-01", valid: false},
		{in: "v1.0.0-1.01", valid: false},
		{in: "v1.0.0-0a", valid: true},
		{in: "v1.0.0-00a", valid: true},
		{in: "v1.0.0+01", valid: true},
		{in: "v1.0.0+00.007", valid: true},
		{in: "v1.0.0--", valid: true},
		{in: "v1.0.0-a-b", valid: true},
		{in: "v1.0.0-a_b", valid: false},
		{in: "v1.0.0+a b", valid: false},
		{in: "v1.0.0-é", valid: false},
		{in: "v1.0.0-a+b+c", valid: false},
		{in: "v1.0.0+b-a", valid: true},
		{in: "v1.0.0-rc.1+build.5", valid: true},
		{in: "v18446744073709551616.0.0", valid: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := semver.IsValid(c.in); got != c.valid {
			t.Errorf("IsValid(%q) = %v, want %v", c.in, got, c.valid)
		}
		//: an invalid string has no pre-release.
		if !c.valid && semver.Prerelease(c.in) != "" {
			t.Errorf("an invalid %q has the pre-release %q", c.in, semver.Prerelease(c.in))
		}
	}
	for _, c := range tests {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCompareEdges pins the orderings a hand-written comparison gets wrong:
// numbers past uint64 compared as numbers rather than overflowing, numeric
// pre-release identifiers compared numerically rather than as text, a number
// below an alphanumeric identifier, build metadata ignored, and invalid
// strings below every version.
func TestCompareEdges(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v, w string
		want int
	}
	tests := []tc{
		{name: "past uint64, patch", v: "v1.2.18446744073709551616", w: "v1.2.18446744073709551615", want: 1},
		{name: "past uint64, major", v: "v18446744073709551616.0.0", w: "v9.9.9", want: 1},
		{name: "past uint64, pre-release", v: "v1.0.0-18446744073709551616", w: "v1.0.0-9", want: 1},
		{name: "digits are numbers", v: "v1.0.0-2", w: "v1.0.0-10", want: -1},
		{name: "a number below a word", v: "v1.0.0-99", w: "v1.0.0-a", want: -1},
		{name: "a digit-led word is a word", v: "v1.0.0-1a", w: "v1.0.0-2", want: 1},
		{name: "ASCII order, upper before lower", v: "v1.0.0-Z", w: "v1.0.0-a", want: -1},
		{name: "hyphen before digits in ASCII", v: "v1.0.0--", w: "v1.0.0-0a", want: -1},
		{name: "the longer list is higher", v: "v1.0.0-a.b", w: "v1.0.0-a", want: 1},
		{name: "build metadata ignored", v: "v1.0.0+a", w: "v1.0.0+b", want: 0},
		{name: "a shorthand is its long form", v: "v2", w: "v2.0.0+anything", want: 0},
		{name: "invalid below a version", v: "garbage", w: "v0.0.0-0", want: -1},
		{name: "a version above invalid", v: "v0.0.0", w: "1.0.0", want: 1},
		{name: "two invalid strings tie", v: "garbage", w: "1.0.0", want: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := semver.Compare(c.v, c.w); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.v, c.w, got, c.want)
		}
		if got := semver.Compare(c.w, c.v); got != -c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.w, c.v, got, -c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// sign reduces an integer to -1, 0 or +1.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
