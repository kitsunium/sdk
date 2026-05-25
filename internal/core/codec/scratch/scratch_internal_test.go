package scratch

import (
	"bytes"
	"sync"
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

// Test_poolGet covers both branches of the pool-invariant guard: the
// nominal type-match pass-through and the panic on a pool whose New yields
// the wrong type. Each case uses its own local *sync.Pool so the package
// pools are never disturbed and the test stays parallel-safe.
func Test_poolGet(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		newFn     func() any
		wantPanic bool
	}
	tests := []tc{
		{"valid buffer", func() any { return new(bytes.Buffer) }, false},
		{"wrong type panics", func() any { return "not a buffer" }, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: a local pool isolates the assertion from the package pools.
		p := &sync.Pool{New: tc.newFn}
		//: recover converts the expected panic into an assertion.
		defer func() {
			if (recover() != nil) != tc.wantPanic {
				t.Errorf("%s: panicked != want %v", tc.name, tc.wantPanic)
			}
		}()
		//: a non-panicking call must hand back a usable value.
		if got := poolGet[*bytes.Buffer](p); !tc.wantPanic && got == nil {
			t.Errorf("%s: got nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
