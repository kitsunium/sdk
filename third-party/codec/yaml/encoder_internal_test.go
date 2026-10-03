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

// Test_yamlEncoder_Close exercises the Close wrapper on both the success
// path (buffer-backed encoder) and the failure path. yaml.v3 surfaces a
// writer failure on both Encode and Close, so a writer that fails on every
// Write makes Close return a wrapped error — the branch the success-only
// case never reaches. The encode result is asserted per case (it must
// succeed on the buffer path and fail on the writer path) so no error is
// silently discarded.
func Test_yamlEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name          string
		build         func() *yamlEncoder
		wantEncodeErr bool
		wantCloseErr  bool
	}
	tests := []tc{
		{
			"close after encode succeeds",
			func() *yamlEncoder { return &yamlEncoder{inner: goyaml.NewEncoder(&bytes.Buffer{})} },
			false,
			false,
		},
		{
			"close over failing writer surfaces wrap",
			func() *yamlEncoder { return &yamlEncoder{inner: goyaml.NewEncoder(errWriter{})} },
			true,
			true,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := tc.build()
		//: assert the encode outcome explicitly so the error is handled,
		//: not discarded — yaml.v3 pushes to the writer eagerly enough that
		//: the failing-writer case errors here too.
		if eerr := enc.Encode(map[string]int{"k": 1}); (eerr != nil) != tc.wantEncodeErr {
			t.Fatalf("%s: Encode err=%v wantErr=%v", tc.name, eerr, tc.wantEncodeErr)
		}
		//: Close flushes remaining buffered state; over the failing writer it
		//: surfaces the wrapped MARSHAL_FAILED branch.
		if cerr := enc.Close(); (cerr != nil) != tc.wantCloseErr {
			t.Errorf("%s: Close err=%v wantErr=%v", tc.name, cerr, tc.wantCloseErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
