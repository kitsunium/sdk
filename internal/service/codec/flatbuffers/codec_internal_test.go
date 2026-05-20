package flatbuffers

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeProvider is the minimal BytesProvider used to exercise the
// structured-source branch of resolveSourceBytes.
type fakeProvider struct {
	payload []byte
}

// compile-time check that fakeProvider satisfies BytesProvider.
var _ BytesProvider = (*fakeProvider)(nil)

// Bytes returns the canned payload.
func (f *fakeProvider) Bytes() []byte {
	//: hand back the canned payload verbatim.
	return f.payload
}

// Test_flatbuffersCodec_Name covers the canonical identifier returned by the codec.
func Test_flatbuffersCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "flatbuffers"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &flatbuffersCodec{}
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

// Test_flatbuffersCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_flatbuffersCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/x-flatbuffers"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &flatbuffersCodec{}
		mimes := c.MIMETypes()
		if len(mimes) == 0 || mimes[0] != tc.wantHead {
			t.Errorf("%s: MIMETypes=%v want head %q", tc.name, mimes, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_flatbuffersCodec_Extensions covers the extension list.
func Test_flatbuffersCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".fbs"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &flatbuffersCodec{}
		exts := c.Extensions()
		if len(exts) == 0 || exts[0] != tc.wantHead {
			t.Errorf("%s: Extensions=%v want head %q", tc.name, exts, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_validateBuffer exercises the size invariants directly.
func Test_validateBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	tests := []tc{
		{"exact header passes", []byte{0, 0, 0, 0}, ""},
		{"empty buffer surfaces FLATBUFFERS_TRUNCATED", nil, "FLATBUFFERS_TRUNCATED"},
		{"three bytes surfaces FLATBUFFERS_TRUNCATED", []byte{1, 2, 3}, "FLATBUFFERS_TRUNCATED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := validateBuffer(tc.data)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: validateBuffer err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_resolveSourceBytes exercises every branch of the source resolver.
func Test_resolveSourceBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		want    []byte
		wantErr string
	}
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tests := []tc{
		{"byte slice passthrough", canned, canned, ""},
		{"BytesProvider passthrough", &fakeProvider{payload: canned}, canned, ""},
		{"int surfaces FLATBUFFERS_BAD_TYPE", 42, nil, "FLATBUFFERS_BAD_TYPE"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := resolveSourceBytes(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: resolveSourceBytes err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
		if tc.wantErr == "" && string(got) != string(tc.want) {
			t.Errorf("%s: bytes=%v want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
