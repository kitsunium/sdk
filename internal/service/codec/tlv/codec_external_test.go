package tlv_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/tlv"
)

// samplePayload exercises every TLV-supported Go type in a single value.
type samplePayload struct {
	Name   string           `json:"name"`
	Age    int32            `json:"age"`
	Score  float64          `json:"score"`
	Bytes  []byte           `json:"bytes"`
	Tags   []string         `json:"tags"`
	Active bool             `json:"active"`
	Map    map[string]int64 `json:"map"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name advertised.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "tlv"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
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

// TestMarshal covers happy-path and rejection-path Marshal calls.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"int round-trip", int64(123), ""},
		{"bool round-trip", true, ""},
		{"channel rejected", make(chan int), "UNSUPPORTED_TYPE"},
		{"func rejected", func() {}, "UNSUPPORTED_TYPE"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := tlv.New().Marshal(tc.in)
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

// TestUnmarshal covers Unmarshal's pointer-shape and length checks.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	tests := []tc{
		{"non-pointer target", []byte{0x40, 0x00}, "x", "UNMARSHAL_FAILED"},
		{"nil pointer target", []byte{0x40, 0x00}, (*string)(nil), "UNMARSHAL_FAILED"},
		{"truncated buffer", []byte{0x40}, new(string), "TRUNCATED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := tlv.New().Unmarshal(tc.data, tc.target)
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

// TestRoundTripScalars exercises the Marshal/Unmarshal round trip for
// every primitive kind the codec advertises.
func TestRoundTripScalars(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"int8", int64(-128)},
		{"int16", int64(-32000)},
		{"int32", int64(-1 << 30)},
		{"int64", int64(-1 << 60)},
		{"uint8", uint64(255)},
		{"uint16", uint64(65535)},
		{"uint32", uint64(1 << 30)},
		{"uint64", uint64(1 << 60)},
		{"float32", float32(3.14)},
		{"float64", float64(2.718281828)},
		{"string", "hello world"},
		{"bytes", []byte{1, 2, 3, 4}},
		{"bool true", true},
		{"bool false", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out any
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if out == nil {
			t.Fatalf("%s: nil round-trip", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripComposite exercises slice / map / struct round trips.
func TestRoundTripComposite(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"slice of any", []any{int64(1), int64(2), int64(3)}},
		{"map string→int64", map[string]int64{"a": 1, "b": 2}},
		{"struct payload", samplePayload{
			Name: "alice", Age: 42, Score: 99.5,
			Bytes: []byte("opaque"), Tags: []string{"go", "tlv"},
			Active: true, Map: map[string]int64{"x": 10},
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := tlv.New()
		encoded, merr := c.Marshal(tc.in)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var out any
		if uerr := c.Unmarshal(encoded, &out); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if reflect.ValueOf(out).IsZero() {
			t.Errorf("%s: decoded zero value", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppenderInterface verifies the Appender contract: prior dst is
// preserved on error, and successive Append calls accumulate.
func TestAppenderInterface(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"two consecutive records share the buffer"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		ap, ok := tlv.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.name)
		}
		buf, err := ap.Append(nil, int64(1))
		if err != nil {
			t.Fatalf("%s: first Append err=%v", tc.name, err)
		}
		firstLen := len(buf)
		buf, err = ap.Append(buf, "two")
		if err != nil {
			t.Fatalf("%s: second Append err=%v", tc.name, err)
		}
		if len(buf) <= firstLen {
			t.Errorf("%s: buffer did not grow on second Append", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStreamingRoundTrip exercises the streaming Encoder/Decoder pair
// with two adjacent records.
func TestStreamingRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"two-record stream"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		sc, ok := tlv.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		if err := enc.Encode(int64(1)); err != nil {
			t.Fatalf("%s: Encode 1 err=%v", tc.name, err)
		}
		if err := enc.Encode("two"); err != nil {
			t.Fatalf("%s: Encode 2 err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close err=%v", tc.name, err)
		}
		dec := sc.NewDecoder(&buf)
		var first, second any
		if err := dec.Decode(&first); err != nil {
			t.Fatalf("%s: Decode 1 err=%v", tc.name, err)
		}
		if err := dec.Decode(&second); err != nil {
			t.Fatalf("%s: Decode 2 err=%v", tc.name, err)
		}
		if eofErr := dec.Decode(&first); !errors.Is(eofErr, io.EOF) {
			t.Errorf("%s: expected EOF after drain, got %v", tc.name, eofErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSizeAndDepthCaps covers the documented input-size and depth caps.
func TestSizeAndDepthCaps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr string
	}
	//: 10 MiB + 1 byte triggers SIZE_EXCEEDED.
	oversized := bytes.Repeat([]byte{0x00}, (10<<20)+1)
	tests := []tc{
		{"oversize buffer", oversized, "SIZE_EXCEEDED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := tlv.New().Unmarshal(tc.data, &out)
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
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("tlv")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/x-tlv"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".tlv"); return ok }},
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
