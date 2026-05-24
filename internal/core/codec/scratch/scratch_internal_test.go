package scratch

import (
	"bytes"
	"testing"
)

// Test_bufferPool_New verifies the pool's New factory yields a usable,
// empty *bytes.Buffer — the invariant AcquireBuffer's type assertion (and
// its panic guard) relies on. Exercised white-box because bufferPool is
// unexported.
func Test_bufferPool_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"new yields empty buffer"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the New factory is what guarantees Get always returns *bytes.Buffer.
		v := bufferPool.New()
		//: the type the whole package contract depends on.
		buf, ok := v.(*bytes.Buffer)
		if !ok {
			t.Fatalf("%s: New yielded %T, want *bytes.Buffer", tc.name, v)
		}
		//: a freshly built buffer must be empty.
		if buf.Len() != 0 {
			t.Errorf("%s: New buffer not empty: len=%d", tc.name, buf.Len())
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
