package cbor

import (
	"bytes"
	"testing"

	gocbor "github.com/fxamacker/cbor/v2"
)

// Test_cborDecoder_MoreAfterInit confirms a freshly-built decoder reports More
// before any Decode call, matching the streaming-codec contract.
func Test_cborDecoder_MoreAfterInit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want bool
	}
	tests := []tc{
		{"fresh decoder reports More", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &cborDecoder{inner: gocbor.NewDecoder(bytes.NewReader(nil))}
		if got := dec.More(); got != tc.want {
			t.Errorf("%s: More()=%v want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
