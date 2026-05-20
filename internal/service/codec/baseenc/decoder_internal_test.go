package baseenc

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

// Test_baseencDecoder_More reports availability before any Decode call so
// streaming consumers can probe input cheaply.
func Test_baseencDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 reports true", variantBase64},
		{"hex reports true", variantHex},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		d := &baseencDecoder{src: bytes.NewReader(nil), v: tc.v}
		if !d.More() {
			t.Errorf("%s: More() = false before first Decode, want true", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencDecoder_Decode drives the lazy json+base-N decode pipeline
// for every variant. Encoding via the matching encoder lets us assert
// the decoder's lazy-init path roundtrips correctly.
func Test_baseencDecoder_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 decode", variantBase64},
		{"base64url decode", variantBase64URL},
		{"base32 decode", variantBase32},
		{"base16 decode", variantBase16},
		{"hex decode", variantHex},
		{"ascii85 decode", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		//: round-trip a JSON value through the matching encoder so the
		//: decoder receives a payload it can actually drain.
		var enc bytes.Buffer
		raw, err := stdjson.Marshal(map[string]int{"n": 42})
		if err != nil {
			t.Fatalf("%s: seed json.Marshal err=%v", tc.name, err)
		}
		encoded := c.encodeBytes(raw)
		enc.Write(encoded)
		d := c.NewDecoder(&enc)
		var got map[string]int
		if err := d.Decode(&got); err != nil {
			t.Fatalf("%s: Decode err=%v", tc.name, err)
		}
		if got["n"] != 42 {
			t.Errorf("%s: Decode got=%v, want n=42", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencDecoder_codec verifies the stub codec helper carries the
// variant verbatim so streamReader dispatch finds the right branch
// regardless of which registered singleton drove NewDecoder.
func Test_baseencDecoder_codec(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 stub", variantBase64},
		{"base32 stub", variantBase32},
		{"hex stub", variantHex},
		{"ascii85 stub", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		d := &baseencDecoder{v: tc.v}
		//: codec() returns a freshly-allocated stub — never nil by
		//: construction (see baseencDecoder.codec). Asserting non-nil
		//: would be dead-code per KTN-SC-SA5011.
		stub := d.codec()
		if stub.variant != tc.v {
			t.Errorf("%s: codec().variant=%v, want %v", tc.name, stub.variant, tc.v)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
