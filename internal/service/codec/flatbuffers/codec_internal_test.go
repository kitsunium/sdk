package flatbuffers

import (
	"bytes"
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

// fakeAcceptor is the minimal BytesAcceptor used to exercise the
// structured-target branch of Unmarshal.
type fakeAcceptor struct {
	got []byte
}

// compile-time check that fakeAcceptor satisfies BytesAcceptor.
var _ BytesAcceptor = (*fakeAcceptor)(nil)

// SetBytes captures the buffer handed in by the codec.
func (f *fakeAcceptor) SetBytes(data []byte) {
	//: store the slice verbatim; the test inspects this field afterwards.
	f.got = data
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

// Test_flatbuffersCodec_Unmarshal exercises every branch of the internal
// Unmarshal method: truncated buffer, *[]byte fast-path, SetBytesser sink,
// and unsupported target rejection.
func Test_flatbuffersCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		target  func() any
		wantErr string
	}
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	tests := []tc{
		{"truncated buffer surfaces FLATBUFFERS_TRUNCATED", []byte{1, 2}, func() any { var b []byte; return &b }, "FLATBUFFERS_TRUNCATED"},
		{"*[]byte fast-path zero-copy", canned, func() any { var b []byte; return &b }, ""},
		{"BytesAcceptor sink receives payload", canned, func() any { return &fakeAcceptor{} }, ""},
		{"unsupported target surfaces FLATBUFFERS_BAD_TARGET", canned, func() any { var s string; return &s }, "FLATBUFFERS_BAD_TARGET"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &flatbuffersCodec{}
		target := tc.target()
		err := c.Unmarshal(tc.data, target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
			return
		}
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		//: happy path — verify the target actually received the buffer.
		switch dst := target.(type) {
		case *[]byte:
			//: full byte equality, not just length.
			if !bytes.Equal(*dst, tc.data) {
				t.Errorf("%s: *[]byte target got %v want %v", tc.name, *dst, tc.data)
			}
			//: zero-copy contract: the published slice MUST alias tc.data's
			//: backing array (Unmarshal documents zero-copy reference for
			//: *[]byte targets). Compare the addresses of the first element.
			if len(tc.data) > 0 && len(*dst) > 0 && &(*dst)[0] != &tc.data[0] {
				t.Errorf("%s: *[]byte fast-path is not zero-copy (got new backing array)", tc.name)
			}
		case *fakeAcceptor:
			//: full byte equality of the slice handed to the Acceptor.
			if !bytes.Equal(dst.got, tc.data) {
				t.Errorf("%s: fakeAcceptor.got=%v want %v", tc.name, dst.got, tc.data)
			}
		default:
			t.Errorf("%s: unexpected target shape %T", tc.name, target)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_flatbuffersCodec_Append exercises every branch of the internal
// Append method: Marshal-failure rollback (dst untouched), []byte happy
// path, and Bytesser happy path.
func Test_flatbuffersCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		in      any
		wantLen int
		wantErr string
	}
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tests := []tc{
		{"appends []byte onto empty dst", nil, canned, len(canned), ""},
		{"appends BytesProvider onto prefilled dst", []byte{0xAA}, &fakeProvider{payload: canned}, 1 + len(canned), ""},
		{"truncated source surfaces FLATBUFFERS_TRUNCATED and keeps dst", []byte{0xAA}, []byte{1, 2}, 1, "FLATBUFFERS_TRUNCATED"},
		{"unsupported type surfaces FLATBUFFERS_BAD_TYPE and keeps dst", []byte{0xAA, 0xBB}, "nope", 2, "FLATBUFFERS_BAD_TYPE"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &flatbuffersCodec{}
		got, err := c.Append(tc.dst, tc.in)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Append err=%v", tc.name, err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("%s: len(out)=%d want %d", tc.name, len(got), tc.wantLen)
			}
			//: happy path — verify the appended suffix matches the resolved
			//: source bytes, not just total length (catches byte-corruption
			//: regressions with unchanged length).
			src, srcErr := resolveSourceBytes(tc.in)
			if srcErr != nil {
				t.Errorf("%s: resolveSourceBytes unexpectedly failed: %v", tc.name, srcErr)
				return
			}
			if !bytes.Equal(got[len(tc.dst):], src) {
				t.Errorf("%s: appended payload mismatch: got=%v want=%v", tc.name, got[len(tc.dst):], src)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
		//: error-path rollback: dst length must equal the input dst length.
		if len(got) != tc.wantLen {
			t.Errorf("%s: error-path len(out)=%d want %d", tc.name, len(got), tc.wantLen)
		}
		//: error-path content rollback: dst MUST be byte-identical to the
		//: input dst (catches in-place mutations that preserve length).
		if !bytes.Equal(got, tc.dst) {
			t.Errorf("%s: error-path mutated dst: got=%v want=%v", tc.name, got, tc.dst)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
