package scratch_test

import (
	"io"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
)

// TestAcquireBuffer verifies the pool always hands back a clean,
// zero-length buffer — even after a prior caller left bytes in it.
func TestAcquireBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		preload string
	}
	tests := []tc{
		{"fresh", ""},
		{"after dirty release", "stale bytes that must not leak"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: dirty-and-release first so the pool may hand the buffer back.
		if tc.preload != "" {
			//: write then release to seed the pool with a used buffer.
			b := scratch.AcquireBuffer()
			b.WriteString(tc.preload)
			scratch.ReleaseBuffer(b)
		}
		//: the buffer under test must be non-nil and clean.
		got := scratch.AcquireBuffer()
		if got == nil {
			t.Fatalf("%s: AcquireBuffer returned nil", tc.name)
		}
		//: AcquireBuffer Resets before returning, so length is always zero.
		if got.Len() != 0 {
			t.Errorf("%s: buffer not clean: len=%d", tc.name, got.Len())
		}
		//: hand it back so the pool stays balanced.
		scratch.ReleaseBuffer(got)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestReleaseBuffer exercises both release branches: a small buffer is
// repooled, an oversized one is dropped. Neither panics, and the pool
// keeps yielding clean buffers afterward.
func TestReleaseBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		grow int
	}
	tests := []tc{
		{"small repooled", 64},
		{"oversize dropped", scratch.MaxRetainedBufBytes + 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: grow forces cap onto the chosen side of the discard threshold.
		b := scratch.AcquireBuffer()
		b.Grow(tc.grow)
		//: must not panic on either branch.
		scratch.ReleaseBuffer(b)
		//: the pool still yields a clean buffer after the release.
		next := scratch.AcquireBuffer()
		if next.Len() != 0 {
			t.Errorf("%s: post-release buffer not clean: len=%d", tc.name, next.Len())
		}
		scratch.ReleaseBuffer(next)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestBufferPoolConcurrent hammers Acquire/Release from many goroutines so
// the race detector can prove the shared pool is safe for concurrent use.
func TestBufferPoolConcurrent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		goroutines int
		iterations int
	}
	tests := []tc{
		{"32x100", 32, 100},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: WaitGroup fences the stress loop so the test owns its goroutines.
		var wg sync.WaitGroup
		//: fan out goroutines that each cycle the pool repeatedly.
		for range tc.goroutines {
			//: wg.Go handles Add/Done so the worker body stays focused.
			wg.Go(func() {
				//: cycle the pool to maximise cross-goroutine reuse.
				for range tc.iterations {
					//: rent → use → release is the full lifecycle under test.
					b := scratch.AcquireBuffer()
					b.WriteString("payload")
					scratch.ReleaseBuffer(b)
				}
			})
		}
		//: block until every worker is done before the test returns.
		wg.Wait()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestAcquireReader verifies AcquireReader positions a *bytes.Reader at src
// so the bytes round-trip exactly.
func TestAcquireReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
	}
	tests := []tc{
		{"empty", ""},
		{"payload", "hello scratch reader"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: AcquireReader resets the pooled reader onto src.
		r := scratch.AcquireReader([]byte(tc.src))
		//: draining the reader must reproduce src exactly.
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%s: ReadAll err=%v", tc.name, err)
		}
		if string(got) != tc.src {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.src)
		}
		//: hand the reader back so the pool stays balanced.
		scratch.ReleaseReader(r)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestReleaseReader verifies a released reader can be re-acquired and
// re-pointed at fresh input without leaking the previous source.
func TestReleaseReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		first string
		next  string
	}
	tests := []tc{
		{"reuse after release", "first source", "second"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: use then release the reader.
		r := scratch.AcquireReader([]byte(tc.first))
		scratch.ReleaseReader(r)
		//: a subsequent acquire must read only the new source.
		r2 := scratch.AcquireReader([]byte(tc.next))
		got, err := io.ReadAll(r2)
		if err != nil {
			t.Fatalf("%s: ReadAll err=%v", tc.name, err)
		}
		if string(got) != tc.next {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.next)
		}
		scratch.ReleaseReader(r2)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
