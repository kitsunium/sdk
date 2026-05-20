package baseenc

import (
	"bytes"
	"testing"
)

// Test_baseencEncoder_Encode drives the streaming encoder over every
// variant so a regression in the json/base-N pipeline wiring fails
// loudly.
func Test_baseencEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 encode", variantBase64},
		{"base64url encode", variantBase64URL},
		{"base32 encode", variantBase32},
		{"base16 encode", variantBase16},
		{"hex encode", variantHex},
		{"ascii85 encode", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		if err := enc.Encode("payload"); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		//: Close to flush so we can sanity-check non-empty output.
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: Encode+Close produced no bytes", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencEncoder_Close verifies the encoder's Close finalises the
// underlying base-N writer (emitting any padding / Adobe-frame markers)
// before the caller can drain the buffer.
func Test_baseencEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 close emits", variantBase64},
		{"hex close emits", variantHex},
		{"ascii85 close emits", variantASCII85},
		{"base16 close flushes buffered writer", variantBase16},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		if err := enc.Encode("payload"); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: Close did not flush — buffer empty", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
