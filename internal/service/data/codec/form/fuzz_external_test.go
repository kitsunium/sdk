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
