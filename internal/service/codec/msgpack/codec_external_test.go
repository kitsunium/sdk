package msgpack_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

type payload struct {
	Name string `msgpack:"name"`
	Age  int    `msgpack:"age"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "msgpack"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := msgpack.New()
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
		{"unsupported input surfaces MARSHAL_FAILED", make(chan int), "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := msgpack.New().Marshal(tc.in)
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
	data, _ := msgpack.New().Marshal(payload{Name: "a", Age: 1})
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
		{"malformed bytes surface UNMARSHAL_FAILED", []byte{0xc1}, &bad, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := msgpack.New().Unmarshal(tc.data, tc.target)
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
		sc, ok := msgpack.New().(codec.StreamingCodec)
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
	good, _ := msgpack.New().Marshal(payload{Name: "a", Age: 1})
	tests := []tc{
		{"decodes a valid record", good, ""},
		{"corrupt input surfaces UNMARSHAL_FAILED", []byte{0xc1}, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc := msgpack.New().(codec.StreamingCodec)
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
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("msgpack")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/msgpack"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".msgpack"); return ok }},
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
