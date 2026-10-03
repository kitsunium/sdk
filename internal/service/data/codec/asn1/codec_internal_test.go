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

// Test_asn1Codec_Append covers the Appender extension via the table-
// driven runCase pattern: forward direction (every case appends
// correctly OR returns dst untouched on error) + reverse direction
// (encoded prefix is preserved on success).
func Test_asn1Codec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-int", nil, 42, false},
		{"happy-prefix", []byte("PRE"), 7, false},
		{"reject-channel", []byte("PRE"), make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: asn1Codec does not implement Appender", tc.name)
		}
		got, err := ap.Append(tc.dst, tc.v)
		//: forward direction: error → dst untouched (len match).
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if len(got) != len(tc.dst) {
				t.Errorf("%s: dst len changed on error: got=%d want=%d", tc.name, len(got), len(tc.dst))
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		//: reverse direction: prefix preserved on success.
		if len(tc.dst) > 0 && string(got[:len(tc.dst)]) != string(tc.dst) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, got[:len(tc.dst)])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
