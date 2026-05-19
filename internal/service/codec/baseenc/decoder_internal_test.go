package baseenc

import (
	"bytes"
	"testing"
)

// Test_baseencDecoder_More_internal verifies the streaming decoder reports
// availability before any Decode call so consumers can probe input cheaply.
func Test_baseencDecoder_More_internal(t *testing.T) {
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
