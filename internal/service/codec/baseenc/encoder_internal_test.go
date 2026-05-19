package baseenc

import (
	"bytes"
	"testing"
)

// Test_baseencEncoder_EncodeClose_internal drives the streaming encoder
// round-trip surface for every variant so Encode + Close run end-to-end.
func Test_baseencEncoder_EncodeClose_internal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 encode close", variantBase64},
		{"hex encode close", variantHex},
		{"ascii85 encode close", variantASCII85},
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
			t.Errorf("%s: encoder produced no bytes", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
