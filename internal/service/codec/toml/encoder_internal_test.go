package toml

import (
	"bytes"
	"testing"

	gotoml "github.com/pelletier/go-toml/v2"
)

// Test_tomlEncoder_Close verifies Close is safe to call; underlying libraries
// either flush or are no-ops and must not panic.
func Test_tomlEncoder_Close(t *testing.T) {
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
		enc := &tomlEncoder{inner: gotoml.NewEncoder(&bytes.Buffer{})}
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
