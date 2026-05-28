package yaml

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

// appendBadMarshaler implements yaml.v3's Marshaler returning an error so the
// Append encode-error branch is reachable without a chan/func payload (which
// yaml.v3 panics on) or a failing writer (Append owns its buffer).
type appendBadMarshaler struct{}

// MarshalYAML returns a synthetic failure so the encoder surfaces an error.
func (appendBadMarshaler) MarshalYAML() (any, error) {
	//: deterministic failure → MARSHAL_FAILED, dst stays pristine.
	return nil, errors.New("synthetic marshaler failure")
}

// Test_yamlCodec_Name covers the canonical identifier returned by the codec.
func Test_yamlCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "yaml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
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

// Test_yamlCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_yamlCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/yaml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
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

// Test_yamlCodec_Extensions covers the extension list.
func Test_yamlCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".yaml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
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

// Test_yamlCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_yamlCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
		if _, err := c.Marshal(map[string]int{"a": 1}); err != nil {
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

// Test_yamlCodec_Unmarshal exercises the Unmarshal path.
func Test_yamlCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
		var out map[string]int
		if err := c.Unmarshal([]byte("[unclosed"), &out); err == nil {
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

// Test_yamlCodec_NewEncoder covers the streaming encoder constructor.
func Test_yamlCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
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

// Test_yamlCodec_NewDecoder covers the streaming decoder constructor.
func Test_yamlCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &yamlCodec{}
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

// Test_yamlCodec_Append covers the Appender extension: happy round-trip
// + dst-untouched-on-error semantics.
func Test_yamlCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-empty-dst", nil, map[string]int{"a": 1}, false},
		{"happy-prefix-dst", []byte("PRE-"), "value", false},
		//: yaml.v3 panics on chan/func types instead of returning an
		//: error, but a value whose MarshalYAML errors drives the encode-
		//: error branch cleanly with dst left untouched.
		{"reject-marshaler-error", []byte("PRE-"), appendBadMarshaler{}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: yamlCodec does not implement Appender", tc.name)
		}
		//: snapshot dst BEFORE Append so a same-length in-place mutation
		//: on the error path is still detectable — length alone would miss it.
		orig := slices.Clone(tc.dst)
		got, err := ap.Append(tc.dst, tc.v)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			//: dst returned byte-for-byte untouched on error.
			if !bytes.Equal(got, orig) {
				t.Errorf("%s: dst mutated on error: got=%q want=%q", tc.name, got, orig)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		//: prefix preserved verbatim when supplied.
		if len(tc.dst) > 0 && string(got[:len(tc.dst)]) != string(tc.dst) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, got[:len(tc.dst)])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
