package flatbuffers_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/flatbuffers"
)

// flatProvider is the minimal BytesProvider used to exercise the structured
// Marshal path through the public API.
type flatProvider struct {
	payload []byte
}

// Bytes hands back the canned payload.
func (f *flatProvider) Bytes() []byte {
	//: passthrough — return the buffer the caller already populated.
	return f.payload
}

// flatAcceptor is the minimal BytesAcceptor used to exercise the structured
// Unmarshal path through the public API.
type flatAcceptor struct {
	got []byte
}

// SetBytes captures the buffer handed by the codec.
func (f *flatAcceptor) SetBytes(data []byte) {
	//: store the buffer verbatim; shared ownership per Unmarshal contract.
	f.got = data
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "flatbuffers"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := flatbuffers.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
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

// TestMarshal exercises every Marshal branch.
func TestMarshal(t *testing.T) {
	t.Parallel()
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"byte slice passthrough", canned, ""},
		{"BytesProvider passthrough", &flatProvider{payload: canned}, ""},
		{"unsupported type surfaces FLATBUFFERS_BAD_TYPE", 42, "FLATBUFFERS_BAD_TYPE"},
		{"truncated source surfaces FLATBUFFERS_TRUNCATED", []byte{1, 2}, "FLATBUFFERS_TRUNCATED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := flatbuffers.New().Marshal(tc.in)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Marshal err=%v", tc.name, err)
			}
			if len(got) == 0 {
				t.Errorf("%s: Marshal returned empty bytes", tc.name)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
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

// TestUnmarshal exercises every Unmarshal branch.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	type tc struct {
		name    string
		data    []byte
		target  func() any
		wantErr string
	}
	tests := []tc{
		{"*[]byte zero-copy reference", canned, func() any { var b []byte; return &b }, ""},
		{"BytesAcceptor sink", canned, func() any { return &flatAcceptor{} }, ""},
		{"truncated buffer surfaces FLATBUFFERS_TRUNCATED", []byte{1}, func() any { var b []byte; return &b }, "FLATBUFFERS_TRUNCATED"},
		{"wrong target surfaces FLATBUFFERS_BAD_TARGET", canned, func() any { var s string; return &s }, "FLATBUFFERS_BAD_TARGET"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := flatbuffers.New().Unmarshal(tc.data, tc.target())
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
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

// TestAppend covers both happy paths plus the truncated-source rejection.
func TestAppend(t *testing.T) {
	t.Parallel()
	canned := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	type tc struct {
		name    string
		dst     []byte
		in      any
		wantLen int
		wantErr string
	}
	tests := []tc{
		{"appends []byte onto empty dst", nil, canned, len(canned), ""},
		{"appends BytesProvider onto prefilled dst", []byte{0xAA}, &flatProvider{payload: canned}, 1 + len(canned), ""},
		{"truncated source surfaces FLATBUFFERS_TRUNCATED", []byte{0xAA}, []byte{1, 2}, 1, "FLATBUFFERS_TRUNCATED"},
		{"unsupported type surfaces FLATBUFFERS_BAD_TYPE", nil, "nope", 0, "FLATBUFFERS_BAD_TYPE"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		appender, ok := flatbuffers.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.name)
		}
		got, err := appender.Append(tc.dst, tc.in)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Append err=%v", tc.name, err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("%s: len(out)=%d want %d", tc.name, len(got), tc.wantLen)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
		//: error path returns dst untouched at its prior length.
		if len(got) != tc.wantLen {
			t.Errorf("%s: error-path len(out)=%d want %d", tc.name, len(got), tc.wantLen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTypeAssertions verifies the codec satisfies Codec + Appender but
// deliberately does NOT satisfy StreamingCodec.
func TestTypeAssertions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		assertion func(codec.Codec) bool
		want      bool
	}
	tests := []tc{
		{"satisfies codec.Codec", func(c codec.Codec) bool { _, ok := any(c).(codec.Codec); return ok }, true},
		{"satisfies codec.Appender", func(c codec.Codec) bool { _, ok := c.(codec.Appender); return ok }, true},
		{"does NOT satisfy codec.StreamingCodec", func(c codec.Codec) bool { _, ok := c.(codec.StreamingCodec); return ok }, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := tc.assertion(flatbuffers.New()); got != tc.want {
			t.Errorf("%s: assertion=%v want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("flatbuffers")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/x-flatbuffers"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".fbs"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
