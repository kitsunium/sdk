package asn1

import "testing"

// Test_asn1Codec_Metadata covers the three metadata methods (Name,
// MIMETypes, Extensions) with a single table-driven suite exercising the
// unexported singleton directly (white-box).
func Test_asn1Codec_Metadata(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	c := &asn1Codec{}
	tests := []tc{
		{"Name returns canonical identifier", c.Name(), "asn1-der"},
		{"MIMETypes first entry is canonical", c.MIMETypes()[0], "application/pkix-cert"},
		{"Extensions first entry is canonical", c.Extensions()[0], ".der"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
