package jsonenc_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/jsonenc"
)

// ExampleAppendString quotes a string with a quote, a newline and a character
// JavaScript would end a line on.
func ExampleAppendString() {
	fmt.Println(string(jsonenc.AppendString(nil, "say \"hi\"\n<b>\u2028")))
	// Output:
	// "say \"hi\"\n<b>\u2028"
}

// ExampleBeginObject writes members in the order they are named.
func ExampleBeginObject() {
	out := jsonenc.BeginObject(nil).
		String("level", "info").
		Int("n", -7).
		Bool("ok", true).
		Null("err").
		Raw("nested", jsonenc.BeginObject(nil).Uint("id", 9).End()).
		End()
	fmt.Println(string(out))
	// Output:
	// {"level":"info","n":-7,"ok":true,"err":null,"nested":{"id":9}}
}

// TestAppendStringExact pins the bytes written for each class of input.
func TestAppendStringExact(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in, want string }{
		{"empty", "", `""`},
		{"plain", "hello", `"hello"`},
		{"quote and backslash", `a"b\c`, `"a\"b\\c"`},
		{"short escapes", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"other control", "\x00\x01\x1f", `"\u0000\u0001\u001f"`},
		{"del is not escaped", "a\x7fb", "\"a\x7fb\""},
		{"html is not escaped", "<a href=\"x\">&</a>", `"<a href=\"x\">&</a>"`},
		{"valid multibyte", "café 東京 😀", `"café 東京 😀"`},
		{"line separator", "a\u2028b", `"a\u2028b"`},
		{"paragraph separator", "a\u2029b", `"a\u2029b"`},
		{"neighbours of the separators", "‧‪", "\"‧‪\""},
		{"bad byte", "a\xffb", "\"a�b\""},
		{"bad continuation", "\xc3(", "\"�(\""},
		{"truncated rune", "ab\xe6\x9d", "\"ab��\""},
		{"surrogate", "\xed\xa0\x80", "\"���\""},
		{"overlong", "\xc0\xaf", "\"��\""},
		{"real replacement char", "�", "\"�\""},
		{"escape after multibyte", "é\"", `"é\""`},
		{"multibyte after escape", "\"é", `"\"é"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(jsonenc.AppendString(nil, tc.in)); got != tc.want {
				t.Fatalf("AppendString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAppendStringKeepsPrefix: the destination is appended to, never rewritten.
func TestAppendStringKeepsPrefix(t *testing.T) {
	t.Parallel()
	got := jsonenc.AppendString([]byte("pre:"), "x\"")
	if string(got) != `pre:"x\""` {
		t.Fatalf("got %q", got)
	}
	if got := string(jsonenc.AppendEscaped([]byte("a"), "b\n")); got != `ab\n` {
		t.Fatalf("AppendEscaped = %q", got)
	}
}

// TestAppendInt covers the extremes of both widths.
func TestAppendInt(t *testing.T) {
	t.Parallel()
	if got := string(jsonenc.AppendInt(nil, -1<<63)); got != "-9223372036854775808" {
		t.Errorf("min int64 = %s", got)
	}
	if got := string(jsonenc.AppendInt([]byte("x"), 0)); got != "x0" {
		t.Errorf("zero = %s", got)
	}
	if got := string(jsonenc.AppendUint(nil, 1<<64-1)); got != "18446744073709551615" {
		t.Errorf("max uint64 = %s", got)
	}
}

// TestPositionsAcrossWordBoundaries puts every byte value at every offset of a
// 40-byte plain string, so each lane of each word, and the tail loop, sees it,
// and holds the result to encoding/json after decoding.
func TestPositionsAcrossWordBoundaries(t *testing.T) {
	t.Parallel()
	const length = 40
	for b := range 256 {
		for pos := range length {
			in := []byte(strings.Repeat("a", length))
			in[pos] = byte(b)
			checkAgainstStdlib(t, string(in))
		}
	}
}

// TestEveryPairOfBytes holds all 65 536 two-byte strings, framed by plain
// text, to encoding/json: every truncated, overlong and surrogate shape of two
// bytes is in it.
func TestEveryPairOfBytes(t *testing.T) {
	t.Parallel()
	for a := range 256 {
		for b := range 256 {
			checkAgainstStdlib(t, "ab"+string([]byte{byte(a), byte(b)})+"cd")
		}
	}
}

// checkAgainstStdlib holds AppendString's answer for in to the properties the
// package promises: it is valid JSON and valid UTF-8, it carries no raw line
// separator, and decoded it is the string encoding/json would have decoded.
func checkAgainstStdlib(t *testing.T, in string) {
	t.Helper()
	got := jsonenc.AppendString(nil, in)
	if !json.Valid(got) {
		t.Fatalf("AppendString(%q) = %q is not valid JSON", in, got)
	}
	if !utf8.Valid(got) {
		t.Fatalf("AppendString(%q) = %q is not valid UTF-8", in, got)
	}
	if bytes.Contains(got, []byte("\u2028")) || bytes.Contains(got, []byte("\u2029")) {
		t.Fatalf("AppendString(%q) = %q carries a raw separator", in, got)
	}
	var decoded, want string
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decoding %q: %v", got, err)
	}
	ref, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal(%q): %v", in, err)
	}
	if err := json.Unmarshal(ref, &want); err != nil {
		t.Fatalf("decoding stdlib %q: %v", ref, err)
	}
	if decoded != want {
		t.Fatalf("AppendString(%q) decodes to %q, encoding/json to %q", in, decoded, want)
	}
	//: valid input is also byte-for-byte what encoding/json writes when HTML
	//: escaping is off, replacement characters aside (none in valid input).
	if utf8.ValidString(in) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(in); err != nil {
			t.Fatal(err)
		}
		if exact := strings.TrimSuffix(buf.String(), "\n"); exact != string(got) {
			t.Fatalf("AppendString(%q) = %s, Encoder(no HTML) = %s", in, got, exact)
		}
	}
}

// FuzzAppendString holds AppendString to encoding/json for any input.
func FuzzAppendString(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "a\"b", "\\", "\x00\x1f", "é", "東京", "\u2028\u2029", "\xff", "ab\xe6\x9d", "\xed\xa0\x80",
		strings.Repeat("a", 31) + "\"", strings.Repeat("é", 20) + "\xff" + strings.Repeat("a", 9),
		"<script>&</script>", "�", "😀\xf0\x9f",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkAgainstStdlib(t, in)
		//: the body is the literal without its quotes.
		if body := jsonenc.AppendEscaped(nil, in); string(jsonenc.AppendString(nil, in)) != `"`+string(body)+`"` {
			t.Fatalf("AppendEscaped(%q) = %q disagrees with AppendString", in, body)
		}
	})
}

// TestObject writes every member kind in order and holds the result to
// encoding/json: valid, in the named order, and decoding to what was written.
func TestObject(t *testing.T) {
	t.Parallel()
	out := jsonenc.BeginObject([]byte("pre")).
		String("z", "last-named-first").
		String("a\"key", "v\n").
		Int("i", -1).
		Uint("u", 1<<63).
		Bool("t", true).
		Bool("f", false).
		Null("n").
		Raw("r", []byte(`[1,2]`)).
		End()
	want := `pre{"z":"last-named-first","a\"key":"v\n","i":-1,"u":9223372036854775808,"t":true,"f":false,"n":null,"r":[1,2]}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	var m map[string]any
	if err := json.Unmarshal(out[len("pre"):], &m); err != nil || len(m) != 8 {
		t.Fatalf("decoded %v, %v", m, err)
	}
}

// TestObjectEmpty: an object with no member is {}.
func TestObjectEmpty(t *testing.T) {
	t.Parallel()
	if got := string(jsonenc.BeginObject(nil).End()); got != "{}" {
		t.Fatalf("got %s", got)
	}
}

// TestObjectValueSemantics: a stale Object is a snapshot, which is what the
// doc says, and the copy that continues is unaffected by it.
func TestObjectValueSemantics(t *testing.T) {
	t.Parallel()
	base := jsonenc.BeginObject(nil).Int("a", 1)
	left := base.Int("b", 2).End()
	if string(left) != `{"a":1,"b":2}` {
		t.Fatalf("got %s", left)
	}
}

// TestMultibyteSequences holds every shape of two-, three- and four-byte
// sequence — each lead byte from C0 to FF followed by the continuation and
// near-continuation values that separate valid, overlong, surrogate, too-high
// and truncated forms, the two separators among them — to encoding/json, in a
// short string (the byte loop) and between two long runs (the word search and
// the dense window).
func TestMultibyteSequences(t *testing.T) {
	t.Parallel()
	edges := []byte{0x00, 0x22, 0x5c, 0x7f, 0x80, 0x81, 0x8f, 0x90, 0x9f, 0xa0, 0xa8, 0xa9, 0xbf, 0xc0, 0xc2, 0xe2, 0xff}
	long := strings.Repeat("a", 40)
	check := func(seq string) {
		checkAgainstStdlib(t, seq)
		checkAgainstStdlib(t, "x"+seq+"y")
		checkAgainstStdlib(t, long+seq+long)
		checkAgainstStdlib(t, seq+seq+long+seq)
	}
	for lead := 0xc0; lead <= 0xff; lead++ {
		for _, b := range edges {
			check(string([]byte{byte(lead), b}))
			for _, c := range edges {
				check(string([]byte{byte(lead), b, c}))
				for _, d := range edges {
					check(string([]byte{byte(lead), b, c, d}))
				}
			}
		}
	}
}
