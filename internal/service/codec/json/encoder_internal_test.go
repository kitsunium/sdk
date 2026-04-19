package json

import (
	"bytes"
	"testing"

	stdjson "encoding/json"
)

// Test_jsonEncoder_Close verifies Close is safe to call; underlying libraries
// either flush or are no-ops and must not panic.
func Test_jsonEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr bool
	}
	tests := []tc{
		{"Close returns nil", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &jsonEncoder{inner: stdjson.NewEncoder(&bytes.Buffer{})}
		if err := enc.Close(); (err != nil) != tc.wantErr {
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
