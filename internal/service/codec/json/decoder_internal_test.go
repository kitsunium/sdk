package json

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

// Test_jsonDecoder_MoreAfterInit confirms a decoder fed an empty reader
// reports More()=false (nothing to decode).
func Test_jsonDecoder_MoreAfterInit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
		want bool
	}
	tests := []tc{
		{"empty reader has nothing to decode", "", false},
		{"object payload has content", `{"a":1}`, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &jsonDecoder{inner: stdjson.NewDecoder(bytes.NewReader([]byte(tc.data)))}
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
