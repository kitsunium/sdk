package form

import (
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// byteValueCount is the size of the single-byte sweep: every value a byte can
// take. Sweeping all of them is what makes the stdlib-equivalence claim total
// rather than anecdotal.
const byteValueCount int = 256

// mixtureCount is the capacity headroom reserved for the multi-byte cases
// appended after the sweep.
const mixtureCount int = 16

// escapeCorpus returns the strings every escaping test runs over: each
// byteValueCount value on its own, then the adversarial mixtures. There is no
// byte appendQueryEscape has not been compared against the stdlib on.
func escapeCorpus() []string {
	//: single-byte strings + the handful of mixed cases below.
	corpus := make([]string, 0, byteValueCount+mixtureCount)
	//: every byte value, alone, including NUL and the high half.
	for b := range byteValueCount {
		//: string(rune(b)) would UTF-8-encode; index a byte slice instead.
		corpus = append(corpus, string([]byte{byte(b)}))
	}
	//: mixtures that exercise the interesting transitions.
	corpus = append(corpus,
		"",
		"plain",
		"a b",
		"  ",
		"a+b",
		"a%20b",
		"%",
		"%%",
		"a=b&c=d",
		"-._~",
		"kitsunium/sdk",
		"héllo wörld",
		"日本語",
		"\x00\xff\x7f",
		strings.Repeat("a b&c=", 32),
	)
	//: caller iterates.
	return corpus
}

// TestAppendQueryEscapeMatchesStdlib pins the hand-rolled escaper against
// net/url.QueryEscape over every byte value and a set of mixtures. The
// hand-rolled version exists only to avoid one allocation per key and per
// value; the moment it disagrees with the stdlib it is a different format,
// so this equivalence is the reason the optimisation is allowed to exist.
func TestAppendQueryEscapeMatchesStdlib(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	//: one subtest per corpus entry, labelled by its quoted form.
	var tests []tc
	for _, s := range escapeCorpus() {
		tests = append(tests, tc{name: "escape " + strconvQuote(s), in: s})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: append onto a non-empty prefix so the helper is also proven not
		//: to clobber bytes the caller already owns.
		const prefix = "PREFIX"
		got := string(appendQueryEscape([]byte(prefix), tc.in))
		want := prefix + url.QueryEscape(tc.in)
		if got != want {
			t.Errorf("%s: appendQueryEscape=%q want %q", tc.name, got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEscapedLenIsExact pins the pre-sizing arithmetic: encodeInto grows its
// buffer exactly once, which is only correct if escapedLen predicts the
// output width to the byte. An under-estimate would silently reintroduce the
// geometric append cascade the single Grow exists to avoid.
func TestEscapedLenIsExact(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	var tests []tc
	for _, s := range escapeCorpus() {
		tests = append(tests, tc{name: "len " + strconvQuote(s), in: s})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := escapedLen(tc.in)
		want := len(appendQueryEscape(nil, tc.in))
		if got != want {
			t.Errorf("%s: escapedLen=%d want %d", tc.name, got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncodeIntoMatchesStdlibEncode pins the whole encoder against
// url.Values.Encode(). Same reasoning as the escaper: encodeInto writes into
// a caller-owned buffer instead of minting a string, and that is the ONLY
// difference it is allowed to have.
func TestEncodeIntoMatchesStdlibEncode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   url.Values
	}
	tests := []tc{
		{"empty", url.Values{}},
		{"single pair", url.Values{"a": {"1"}}},
		{"sorted output from unsorted input", url.Values{"z": {"26"}, "a": {"1"}, "m": {"13"}}},
		{"repeated key keeps slice order", url.Values{"a": {"1", "2", "3"}}},
		{"empty key and empty value", url.Values{"": {""}}},
		{"empty value slice emits nothing", url.Values{"a": {}, "b": {"2"}}},
		{"escaping in both halves", url.Values{"a b&c": {"d=e f", "ünïcode"}}},
		{"value that looks pre-escaped", url.Values{"k": {"%41+%42"}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := string(encodeInto(nil, tc.in))
		want := tc.in.Encode()
		if got != want {
			t.Errorf("%s: encodeInto=%q want %q", tc.name, got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncodedLenIsExact pins encodedLen against the bytes encodeInto really
// writes, including the '&' separator arithmetic that a key bound to an
// empty slice perturbs.
func TestEncodedLenIsExact(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   url.Values
	}
	tests := []tc{
		{"empty", url.Values{}},
		{"single pair", url.Values{"a": {"1"}}},
		{"three keys", url.Values{"a": {"1"}, "b": {"2"}, "c": {"3"}}},
		{"repeated key", url.Values{"a": {"1", "2", "3"}}},
		{"empty value slice", url.Values{"a": {}, "b": {"2"}}},
		{"all empty value slices", url.Values{"a": {}, "b": {}}},
		{"heavy escaping", url.Values{"a b&c": {"d=e f", "ünïcode"}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: encodedLen takes the key set encodeInto builds; order is
		//: irrelevant to the arithmetic, so the raw collection will do.
		keys := slices.Collect(maps.Keys(tc.in))
		got := encodedLen(tc.in, keys)
		want := len(encodeInto(nil, tc.in))
		if got != want {
			t.Errorf("%s: encodedLen=%d want %d", tc.name, got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAsValuesShapes covers the argument-shape gate directly, including the
// nil-pointer branches an external test cannot reach without panicking.
func TestAsValuesShapes(t *testing.T) {
	t.Parallel()
	//: nil typed pointers, declared once so the table stays readable.
	var (
		nilValues *url.Values
		nilMulti  *map[string][]string
		nilSingle *map[string]string
	)
	type tc struct {
		name    string
		in      any
		wantOK  bool
		wantOut url.Values
	}
	tests := []tc{
		{"url.Values", url.Values{"a": {"1"}}, true, url.Values{"a": {"1"}}},
		{"map[string][]string", map[string][]string{"a": {"1"}}, true, url.Values{"a": {"1"}}},
		{"map[string]string widened", map[string]string{"a": "1"}, true, url.Values{"a": {"1"}}},
		{"*url.Values", &url.Values{"a": {"1"}}, true, url.Values{"a": {"1"}}},
		{"*map[string][]string", &map[string][]string{"a": {"1"}}, true, url.Values{"a": {"1"}}},
		{"*map[string]string widened", &map[string]string{"a": "1"}, true, url.Values{"a": {"1"}}},
		{"nil *url.Values", nilValues, false, nil},
		{"nil *map[string][]string", nilMulti, false, nil},
		{"nil *map[string]string", nilSingle, false, nil},
		{"unsupported struct", struct{ A int }{1}, false, nil},
		{"unsupported map[string]any", map[string]any{"a": 1}, false, nil},
		{"untyped nil", nil, false, nil},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := asValues(tc.in)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
		}
		if !tc.wantOK {
			return
		}
		//: encoded form is the cheapest total comparison of two url.Values.
		if gotEnc, wantEnc := got.Encode(), tc.wantOut.Encode(); gotEnc != wantEnc {
			t.Errorf("%s: values=%q want %q", tc.name, gotEnc, wantEnc)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// strconvQuote renders s as a subtest-safe label. strconv.Quote would do, but
// t.Run rewrites spaces into underscores anyway, so a local helper keeps the
// import list to what the assertions actually need.
func strconvQuote(s string) string {
	//: hex-escape everything outside the printable ASCII range so a label
	//: never carries a raw control byte into the test output.
	var b strings.Builder
	//: quote delimiters make an empty string visible as `""`.
	b.WriteByte('"')
	for i := range len(s) {
		//: printable ASCII travels verbatim.
		if s[i] >= 0x20 && s[i] < 0x7f {
			b.WriteByte(s[i])
			continue
		}
		//: everything else renders as \xNN.
		b.WriteString("\\x")
		b.WriteByte(upperhex[s[i]>>4])
		b.WriteByte(upperhex[s[i]&0x0F])
	}
	b.WriteByte('"')
	return b.String()
}
