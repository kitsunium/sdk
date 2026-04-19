package json

import (
	"bytes"
	"testing"

	stdjson "encoding/json"
)

// Test_jsonEncoder_Encode exercises the Encode wrapper against both happy-path
// and failure inputs so the error-case branch is covered.
func Test_jsonEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		wantErr bool
	}
	tests := []tc{
		{"struct encodes cleanly", struct{ A int }{A: 1}, false},
		{"channel triggers failure", make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &jsonEncoder{inner: stdjson.NewEncoder(&bytes.Buffer{})}
		err := enc.Encode(tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Encode err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonEncoder_Close exercises the Close wrapper after a successful Encode.
func Test_jsonEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr bool
	}
	tests := []tc{
		{"close after encode succeeds", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &jsonEncoder{inner: stdjson.NewEncoder(&bytes.Buffer{})}
		if err := enc.Encode(struct{ A int }{A: 1}); err != nil {
			t.Fatalf("%s: Encode setup err=%v", tc.name, err)
		}
		err := enc.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Close err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
