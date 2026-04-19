package msgpack

import (
	"bytes"
	"testing"

	gomsgpack "github.com/vmihailenco/msgpack/v5"
)

// Test_msgpackDecoder_MoreAfterInit confirms a freshly-built decoder reports More
// before any Decode call, matching the streaming-codec contract.
func Test_msgpackDecoder_MoreAfterInit(t *testing.T) {
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
		dec := &msgpackDecoder{inner: gomsgpack.NewDecoder(bytes.NewReader(nil))}
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
