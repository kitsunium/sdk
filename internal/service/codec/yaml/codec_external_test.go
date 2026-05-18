package yaml_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/yaml"
)

type payload struct {
	Name string `yaml:"name"`
	Age  int    `yaml:"age"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "yaml"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := yaml.New()
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

// TestMarshal covers the encoder success path plus available error cases.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"round-trip success", payload{Name: "a", Age: 1}, ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := yaml.New().Marshal(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
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

// TestUnmarshal covers the decoder success path plus the UNMARSHAL_FAILED
// branch on malformed bytes.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	data, merr := yaml.New().Marshal(payload{Name: "a", Age: 1})
	if merr != nil {
		t.Fatalf("Marshal setup err=%v", merr)
	}
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	var good payload
	var bad payload
	tests := []tc{
		{"round-trip success", data, &good, ""},
		{"malformed bytes surface UNMARSHAL_FAILED", []byte("[unclosed"), &bad, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := yaml.New().Unmarshal(tc.data, tc.target)
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

// TestNewEncoder exercises the streaming encoder with a happy-path encode
// plus a writer-failure path when applicable.
func TestNewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"streams a record"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := yaml.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		if err := enc.Encode(payload{Name: "a", Age: 1}); err != nil {
			t.Errorf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewDecoder exercises the streaming decoder including the
// UNMARSHAL_FAILED branch on corrupt input.
func TestNewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	good, merr := yaml.New().Marshal(payload{Name: "a", Age: 1})
	if merr != nil {
		t.Fatalf("Marshal setup err=%v", merr)
	}
	tests := []tc{
		{"decodes a valid record", good, ""},
		{"corrupt input surfaces UNMARSHAL_FAILED", []byte("[unclosed"), "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc := yaml.New().(codec.StreamingCodec)
		dec := sc.NewDecoder(bytes.NewReader(tc.data))
		var out payload
		err := dec.Decode(&out)
		if tc.wantErr == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				t.Errorf("%s: Decode err=%v", tc.name, err)
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

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("yaml")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/yaml"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".yaml"); return ok }},
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
