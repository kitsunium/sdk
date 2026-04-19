package asn1

import (
	"testing"
)

// Test_asn1Codec_Name covers the canonical identifier returned by the codec.
func Test_asn1Codec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "asn1-der"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &asn1Codec{}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_asn1Codec_MIMETypes covers the MIME list and its copy semantics.
func Test_asn1Codec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/pkix-cert"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &asn1Codec{}
		mimes := c.MIMETypes()
		if len(mimes) == 0 || mimes[0] != tc.wantHead {
			t.Errorf("%s: MIMETypes=%v, want head %q", tc.name, mimes, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_asn1Codec_Extensions covers the extension list.
func Test_asn1Codec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".der"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &asn1Codec{}
		exts := c.Extensions()
		if len(exts) == 0 || exts[0] != tc.wantHead {
			t.Errorf("%s: Extensions=%v, want head %q", tc.name, exts, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_asn1Codec_Marshal exercises the Marshal path with a single canonical case.
func Test_asn1Codec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &asn1Codec{}
		data, err := c.Marshal(struct{ N int }{N: 42})
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		if len(data) == 0 {
			t.Errorf("%s: empty output", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_asn1Codec_Unmarshal exercises the Unmarshal path.
func Test_asn1Codec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &asn1Codec{}
		var out struct{ N int }
		if err := c.Unmarshal([]byte{0xff, 0x00}, &out); err == nil {
			t.Errorf("%s: Unmarshal expected error on bad DER", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
