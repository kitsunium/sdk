package baseenc

import (
	"testing"
)

// Test_baseencCodec_spec verifies the spec lookup returns the canonical
// row for each variant — Name / MIME / Extension all derive from this
// table.
func Test_baseencCodec_spec(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
		want string
	}
	tests := []tc{
		{"base64 spec", variantBase64, "base64"},
		{"base64url spec", variantBase64URL, "base64url"},
		{"base32 spec", variantBase32, "base32"},
		{"base16 spec", variantBase16, "base16"},
		{"hex spec", variantHex, "hex"},
		{"ascii85 spec", variantASCII85, "ascii85"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		//: spec() returns a pointer into the package-level variantSpecs
		//: array — it is never nil by construction, so we read .name
		//: directly without a defensive nil check.
		if got := c.spec().name; got != tc.want {
			t.Errorf("%s: spec.name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
