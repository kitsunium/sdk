package codec_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

type event struct {
	Name  string `json:"name" xml:"name"`
	Count int    `json:"count" xml:"count"`
}

// TestMarshal covers the facade Marshal dispatcher against every registered
// Format plus the UNKNOWN_FORMAT failure path.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		value   any
		wantErr string // "" on success, reason string on expected failure
	}
	tests := []tc{
		{"json success", codec.JSON, event{Name: "ping", Count: 3}, ""},
		{"xml success", codec.XML, event{Name: "ping", Count: 1}, ""},
		{"yaml success", codec.YAML, map[string]int{"a": 1}, ""},
		{"toml success", codec.TOML, map[string]any{"a": 1}, ""},
		{"cbor success", codec.CBOR, map[string]int{"a": 1}, ""},
		{"msgpack success", codec.MsgPack, map[string]int{"a": 1}, ""},
		{"unknown format", codec.Format("nope"), 1, "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.Marshal(tc.format, tc.value)
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

// TestUnmarshal covers the facade Unmarshal dispatcher end-to-end including
// the UNKNOWN_FORMAT failure path.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		data    []byte
		wantErr string
	}
	//: produce a JSON payload we can successfully decode in the happy case.
	jsonPayload, mErr := codec.Marshal(codec.JSON, event{Name: "ping", Count: 3})
	if mErr != nil {
		t.Fatalf("seed Marshal err = %v", mErr)
	}
	tests := []tc{
		{"json success", codec.JSON, jsonPayload, ""},
		{"unknown format", codec.Format("nope"), []byte("1"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out event
		err := codec.Unmarshal(tc.format, tc.data, &out)
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

// TestNewEncoder covers the streaming constructor dispatch against a
// streaming codec, a non-streaming codec, and an unknown Format.
func TestNewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		wantErr string
	}
	tests := []tc{
		{"json streaming ok", codec.JSON, ""},
		{"asn1 non-streaming", codec.ASN1DER, "STREAMING_UNSUPPORTED"},
		{"unknown format", codec.Format("nope"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.NewEncoder(tc.format, io.Discard)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: NewEncoder err=%v", tc.name, err)
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

// TestNewDecoder covers the streaming decoder constructor dispatch.
func TestNewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		wantErr string
	}
	tests := []tc{
		{"json streaming ok", codec.JSON, ""},
		{"csv non-streaming", codec.CSV, "STREAMING_UNSUPPORTED"},
		{"unknown format", codec.Format("nope"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.NewDecoder(tc.format, strings.NewReader(""))
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: NewDecoder err=%v", tc.name, err)
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

// TestRoundTripJSON exercises an end-to-end streaming JSON round-trip
// through the facade (several records written then read back).
func TestRoundTripJSON(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		records []event
	}
	tests := []tc{
		{"two records", []event{{Name: "a", Count: 1}, {Name: "b", Count: 2}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		enc, err := codec.NewEncoder(codec.JSON, &buf)
		if err != nil {
			t.Fatalf("%s: NewEncoder: %v", tc.name, err)
		}
		for _, e := range tc.records {
			if eerr := enc.Encode(e); eerr != nil {
				t.Fatalf("%s: Encode: %v", tc.name, eerr)
			}
		}
		if cerr := enc.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", tc.name, cerr)
		}
		dec, derr := codec.NewDecoder(codec.JSON, &buf)
		if derr != nil {
			t.Fatalf("%s: NewDecoder: %v", tc.name, derr)
		}
		var got []event
		for dec.More() {
			var e event
			if err := dec.Decode(&e); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("%s: Decode: %v", tc.name, err)
			}
			got = append(got, e)
		}
		if len(got) != len(tc.records) {
			t.Errorf("%s: decoded %d records, want %d", tc.name, len(got), len(tc.records))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestFromMIME covers direct matches, parameter-bearing MIME headers, and
// unknown-MIME misses.
func TestFromMIME(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantF  codec.Format
		wantOK bool
	}
	tests := []tc{
		{"direct json", "application/json", codec.JSON, true},
		{"charset parameter", "application/json; charset=utf-8", codec.JSON, true},
		{"uppercase header", "APPLICATION/JSON", codec.JSON, true},
		{"trailing whitespace", "application/xml ", codec.XML, true},
		{"multiple params", "application/cbor; boundary=xyz; q=0.9", codec.CBOR, true},
		{"unknown misses", "unknown", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := codec.FromMIME(tc.in)
		if ok != tc.wantOK || got != tc.wantF {
			t.Errorf("%s: FromMIME(%q) = (%q, %v) want (%q, %v)",
				tc.name, tc.in, got, ok, tc.wantF, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestFromExtension covers extension → Format resolution.
func TestFromExtension(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantF  codec.Format
		wantOK bool
	}
	tests := []tc{
		{"xml extension", ".xml", codec.XML, true},
		{"yaml alias", ".yml", codec.YAML, true},
		{"unknown misses", ".zzz", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := codec.FromExtension(tc.in)
		if ok != tc.wantOK || got != tc.wantF {
			t.Errorf("%s: FromExtension(%q) = (%q, %v) want (%q, %v)",
				tc.name, tc.in, got, ok, tc.wantF, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAvailable asserts every M1 Format is present in Available().
func TestAvailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want codec.Format
	}
	tests := []tc{
		{"json", codec.JSON},
		{"ndjson", codec.NDJSON},
		{"xml", codec.XML},
		{"csv", codec.CSV},
		{"asn1-der", codec.ASN1DER},
		{"pem", codec.PEM},
		{"yaml", codec.YAML},
		{"toml", codec.TOML},
		{"cbor", codec.CBOR},
		{"msgpack", codec.MsgPack},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		list := codec.Available()
		if !slices.Contains(list, tc.want) {
			t.Errorf("%s: Available() missing %q (got %v)", tc.name, tc.want, list)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
