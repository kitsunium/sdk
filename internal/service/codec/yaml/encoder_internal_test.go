package yaml

import (
	"bytes"
	"testing"

	goyaml "gopkg.in/yaml.v3"
)

// Test_yamlEncoder_EncodeClose goes through the happy path — Encode then
// Close — so the wrapper's success path is exercised (Close alone errors
// when nothing has been written yet).
func Test_yamlEncoder_EncodeClose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value any
	}
	tests := []tc{
		{"encode a simple map", map[string]int{"n": 1}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		enc := &yamlEncoder{inner: goyaml.NewEncoder(&buf)}
		if err := enc.Encode(tc.value); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
