package form_test

import (
	"maps"
	"net/url"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/form"
)

// FuzzRoundTrip is #897's form target, ADR 0036's contract: any input decodes
// without a panic; the values it yields re-encode, and the second round trip
// is the fixed point — the same values, and the same bytes on the next encode.
func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []string{"a=1&b=2&a=3", "k=%E2%82%AC+x", "=empty", "a", "a=%zz", "a=1;b=2", ""} {
		f.Add([]byte(seed))
	}
	c := form.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var values url.Values
		//: decoded, or refused — never a panic.
		if err := c.Unmarshal(data, &values); err != nil {
			return
		}
		first, err := c.Marshal(values)
		if err != nil {
			t.Fatalf("Marshal of decoded values: %v", err)
		}
		var again url.Values
		//: the values read back as written.
		if err := c.Unmarshal(first, &again); err != nil || !maps.EqualFunc(again, values, slices.Equal[[]string]) {
			t.Fatalf("round trip of %q: %v (%v), want %v", first, again, err, values)
		}
		second, err := c.Marshal(again)
		//: and the bytes are the fixed point.
		if err != nil || string(second) != string(first) {
			t.Fatalf("second encode %q (%v), want %q", second, err, first)
		}
	})
}

// FuzzAppendPair is #910's form target: whatever bytes a payload holds, and
// whatever key it is bound to, AppendPair writes the bytes net/url's Encode
// writes for that one pair — so the facade's promotion, which appends the
// JSON of a value this way, can never drift from the codec's own escaping.
func FuzzAppendPair(f *testing.F) {
	for _, seed := range []string{"", "plain", "a b&c=d", `{"id":7,"name":"ada"}`, "\x00\xff\x7f", "日本語", "%zz+"} {
		f.Add("_json", []byte(seed))
	}
	f.Fuzz(func(t *testing.T, key string, value []byte) {
		got := string(form.AppendPair(nil, key, value))
		if want := (url.Values{key: {string(value)}}).Encode(); got != want {
			t.Fatalf("AppendPair(%q, %q) = %q, want %q", key, value, got, want)
		}
	})
}
