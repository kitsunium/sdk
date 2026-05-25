package scratch

import "testing"

// Test_bufferPool_recyclesCleanBuffer verifies the package-level bufferPool
// (unexported) hands out a clean, zero-length *bytes.Buffer — the invariant
// AcquireBuffer relies on. White-box because bufferPool is unexported.
func Test_bufferPool_recyclesCleanBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"get yields an empty buffer"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: Get drives the recycler factory on a cold pool.
		buf := bufferPool.Get()
		if buf == nil {
			t.Fatalf("%s: bufferPool.Get returned nil", tc.name)
			return
		}
		//: a recycled or freshly built buffer must be empty.
		if buf.Len() != 0 {
			t.Errorf("%s: buffer not empty: len=%d", tc.name, buf.Len())
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_readerPool_recyclesReader verifies the package-level readerPool
// (unexported) hands out a usable *bytes.Reader.
func Test_readerPool_recyclesReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"get yields a reader"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: Get drives the recycler factory on a cold pool.
		if r := readerPool.Get(); r == nil {
			t.Errorf("%s: readerPool.Get returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
