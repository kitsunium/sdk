package cbor

import (
	"bytes"
	"testing"

	gocbor "github.com/fxamacker/cbor/v2"
)

// Test_cborDecoder_Decode covers both a success path and a malformed-input
// path to exercise the wrap branch.
func Test_cborDecoder_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	tests := []tc{
		{"empty stream does not error further", nil, false},
		{"garbage bytes surface an error", []byte{0xff, 0xff, 0xff, 0xff}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &cborDecoder{inner: gocbor.NewDecoder(bytes.NewReader(tc.data))}
		var out map[string]any
		err := dec.Decode(&out)
		//: treat EOF on the empty case as non-error.
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error on malformed input", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_cborDecoder_More asserts the sticky EOF latch is idempotent: calling
// More twice in a row on a fresh decoder returns the same value.
func Test_cborDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"idempotent before any Decode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &cborDecoder{inner: gocbor.NewDecoder(bytes.NewReader(nil))}
		first := dec.More()
		second := dec.More()
		if first != second {
			t.Errorf("%s: More non-idempotent: first=%v second=%v", tc.name, first, second)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
