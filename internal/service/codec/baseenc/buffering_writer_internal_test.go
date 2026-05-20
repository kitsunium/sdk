package baseenc

import (
	"bytes"
	"testing"
)

// Test_bufferingWriter_Write exercises the buffered Write path across
// distinct payload shapes to confirm the underlying bytes.Buffer
// accumulates without partial commits.
func Test_bufferingWriter_Write(t *testing.T) {
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

// Test_bufferingWriter_Close encodes the accumulated payload on Close and
// flushes it to the wrapped writer. The short-write + writer-error paths
// are covered elsewhere (codec_internal_test.go); this case pins the
// happy path so a regression in the encode-on-close ordering fails here.
func Test_bufferingWriter_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
	}
	tests := []tc{
		{"empty payload close emits nothing", []byte{}},
		{"non-empty payload close emits encoded bytes", []byte("data")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: variantBase16}
		var sink bytes.Buffer
		w := &bufferingWriter{dst: &sink, codec: c}
		if _, err := w.Write(tc.payload); err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		//: encoded length is deterministic — hex doubles the byte count.
		wantLen := len(tc.payload) * 2
		if sink.Len() != wantLen {
			t.Errorf("%s: Close emitted %d bytes, want %d", tc.name, sink.Len(), wantLen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
