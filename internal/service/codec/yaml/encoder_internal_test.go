package yaml

import (
	"bytes"
	"errors"
	"testing"

	goyaml "gopkg.in/yaml.v3"
)

// errWriter always fails — used to force yaml.v3's Encoder to surface an
// error without relying on unsupported-type payloads (yaml.v3 panics on
// channels / funcs rather than returning an error).
type errWriter struct{}

// Write returns a synthetic failure so the Encoder wrapper propagates the
// MARSHAL_FAILED wrap.
//
// Params:
//   - _: payload bytes, ignored.
//
// Returns:
//   - n: always 0.
//   - err: always non-nil.
func (errWriter) Write(_ []byte) (n int, err error) {
	//: predictable failure for the encoder wrapper to propagate.
	return 0, errors.New("synthetic writer failure")
}

// Test_yamlEncoder_Encode covers happy-path and failure-path Encode calls.
func Test_yamlEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		build   func() *yamlEncoder
		wantErr bool
	}
	tests := []tc{
		{
			"map encodes cleanly",
			func() *yamlEncoder {
				return &yamlEncoder{inner: goyaml.NewEncoder(&bytes.Buffer{})}
			},
			false,
		},
		{
			"writer error surfaces via wrap",
			func() *yamlEncoder {
				return &yamlEncoder{inner: goyaml.NewEncoder(errWriter{})}
			},
			true,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := tc.build()
		err := enc.Encode(map[string]int{"k": 1})
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

// Test_yamlEncoder_Close exercises the Close wrapper after a successful Encode.
func Test_yamlEncoder_Close(t *testing.T) {
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
		var buf bytes.Buffer
		enc := &yamlEncoder{inner: goyaml.NewEncoder(&buf)}
		if err := enc.Encode(map[string]int{"k": 1}); err != nil {
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
