package token

import (
	"encoding/json"
	"strings"
	"testing"
)

// Test_quoteJSONString pins the literal for the shapes a token
// carry, and that what it writes decodes to the string it was given.
func Test_quoteJSONString(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in, want string }{
		{"empty", "", `""`},
		{"alg", "HS256", `"HS256"`},
		{"url", "https://auth.example/realms/prod?a=1&b=<2>", `"https://auth.example/realms/prod?a=1&b=<2>"`},
		{"quote and backslash", `a"b\c`, `"a\"b\\c"`},
		{"control", "a\x01\n\b", `"a\u0001\n\b"`},
		{"separator", "a\u2028", `"a\u2028"`},
		{"long past the scratch buffer", strings.Repeat("ab\"", 100), `"` + strings.Repeat(`ab\"`, 100) + `"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quoteJSONString(tc.in)
			if got != tc.want {
				t.Fatalf("quoteJSONString(%q) = %s, want %s", tc.in, got, tc.want)
			}
			var back string
			if err := json.Unmarshal([]byte(got), &back); err != nil || back != tc.in {
				t.Fatalf("decoded %q, %v; want %q", back, err, tc.in)
			}
		})
	}
}

// Test_quoteJSONStringRepairsInvalidUTF8 pins the declared change: a bad byte
// is written as U+FFFD, so the literal is valid JSON whatever it was given.
func Test_quoteJSONStringRepairsInvalidUTF8(t *testing.T) {
	t.Parallel()
	if got, want := quoteJSONString("a\xffb"), "\"a�b\""; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Test_quoteJSONStrings pins the array: empty, one, several, and one that
// outgrows the scratch buffer.
func Test_quoteJSONStrings(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("aud-", 40)
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"none", nil, `[]`},
		{"one", []string{"a"}, `["a"]`},
		{"several", []string{"a", `b"`, ""}, `["a","b\"",""]`},
		{"past the scratch", []string{long, long}, `["` + long + `","` + long + `"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := quoteJSONStrings(tc.in); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
