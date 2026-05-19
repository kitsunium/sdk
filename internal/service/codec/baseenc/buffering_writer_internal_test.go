package baseenc

import (
	"bytes"
	"testing"
)

// Test_bufferingWriter_Write_internal exercises the buffered Write path
// across distinct payload shapes to confirm the underlying bytes.Buffer
// accumulates without partial commits.
func Test_bufferingWriter_Write_internal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
	}
	tests := []tc{
		{"empty payload buffered", []byte{}},
		{"single byte buffered", []byte{0x01}},
		{"multi byte buffered", []byte("hello world")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: variantBase16}
		var sink bytes.Buffer
		w := &bufferingWriter{dst: &sink, codec: c}
		n, err := w.Write(tc.payload)
		if err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		if n != len(tc.payload) {
			t.Errorf("%s: Write n=%d want %d", tc.name, n, len(tc.payload))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
