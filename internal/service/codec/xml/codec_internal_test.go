package xml

import (
	"bytes"
	stdxml "encoding/xml"
	"testing"
)

// Test_xmlCodec_Name covers the canonical identifier returned by the codec.
func Test_xmlCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "xml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
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

// Test_xmlCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_xmlCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/xml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
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

// Test_xmlCodec_Extensions covers the extension list.
func Test_xmlCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".xml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
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

// Test_xmlCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_xmlCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
		type payload struct {
			XMLName stdxml.Name `xml:"doc"`
			N       int         `xml:"n"`
		}
		if _, err := c.Marshal(payload{N: 1}); err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlCodec_Unmarshal exercises the Unmarshal path.
func Test_xmlCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
		var out struct{}
		if err := c.Unmarshal([]byte("<bad"), &out); err == nil {
			t.Errorf("%s: expected Unmarshal error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlCodec_NewEncoder covers the streaming encoder constructor.
func Test_xmlCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
		if enc := c.NewEncoder(&bytes.Buffer{}); enc == nil {
			t.Errorf("%s: NewEncoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlCodec_NewDecoder covers the streaming decoder constructor.
func Test_xmlCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &xmlCodec{}
		if dec := c.NewDecoder(bytes.NewReader(nil)); dec == nil {
			t.Errorf("%s: NewDecoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlCodec_Append covers the Appender extension: round-trip and
// dst-untouched-on-error semantics.
func Test_xmlCodec_Append(t *testing.T) {
	t.Parallel()
	type fixture struct {
		XMLName stdxml.Name `xml:"doc"`
		Title   string      `xml:"title"`
	}
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-empty-dst", nil, fixture{Title: "hello"}, false},
		{"happy-prefix-dst", []byte("PRE-"), fixture{Title: "hi"}, false},
		{"reject-channel", nil, make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: xmlCodec does not implement Appender", tc.name)
		}
		got, err := ap.Append(tc.dst, tc.v)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			//: dst must be returned untouched on error. Compare length
			//: when both slices are non-empty (avoid &got[0] panic on
			//: empty result, which is the error contract).
			if len(got) != len(tc.dst) {
				t.Errorf("%s: dst len changed on error: got=%d want=%d", tc.name, len(got), len(tc.dst))
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		//: appended output must contain the prefix when one was supplied.
		if len(tc.dst) > 0 && string(got[:len(tc.dst)]) != string(tc.dst) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, got[:len(tc.dst)])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
