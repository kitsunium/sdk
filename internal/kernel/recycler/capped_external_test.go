package recycler_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// capBox is a test value whose reported capacity and reset state are both
// observable, so CappedPool's discard-before-reset policy can be asserted
// directly on a held reference.
type capBox struct {
	cap      int
	wasReset bool
}

// newCappedTestPool builds a CappedPool over *capBox with the given
// threshold; reset flips wasReset, capOf reports the box's cap field.
func newCappedTestPool(maxCap int) *recycler.CappedPool[*capBox] {
	return recycler.NewCappedPool[*capBox](
		func() *capBox { return &capBox{} },
		func(b *capBox) { b.wasReset = true },
		func(b *capBox) int { return b.cap },
		maxCap,
	)
}

// TestNewCappedPool covers the fail-fast constructor contract: nil
// reset/capOf and a non-positive maxCap are programmer errors and panic; a
// valid configuration returns without panicking.
func TestNewCappedPool(t *testing.T) {
	t.Parallel()
	mk := func() *capBox { return &capBox{} }
	tests := []struct {
		name      string
		runner    func()
		wantPanic bool
	}{
		{"nil reset panics", func() { recycler.NewCappedPool[*capBox](mk, nil, func(*capBox) int { return 0 }, 8) }, true},
		{"nil capOf panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, nil, 8) }, true},
		{"zero maxCap panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, func(*capBox) int { return 0 }, 0) }, true},
		{"negative maxCap panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, func(*capBox) int { return 0 }, -1) }, true},
		{"valid configuration does not panic", func() { newCappedTestPool(8) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				//: capture the recovered state once and match the expectation.
				got := recover() != nil
				if got != tc.wantPanic {
					t.Errorf("panic = %v, want %v", got, tc.wantPanic)
				}
			}()
			tc.runner()
		})
	}
}

// TestCappedPool_Get verifies Get hands out a usable value on a cold pool.
func TestCappedPool_Get(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cold pool builds via factory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newCappedTestPool(8)
			if got := r.Get(); got == nil {
				t.Fatal("Get returned nil on cold pool")
			}
		})
	}
}

// TestCappedPool_Put verifies the discard-before-reset policy: a value at
// or under maxCap is reset (and repooled); an over-cap value is orphaned
// WITHOUT reset (so a caller still aliasing its bytes is not clobbered).
func TestCappedPool_Put(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cap       int
		wantReset bool
	}{
		{"under cap is reset and repooled", 4, true},
		{"at cap is reset and repooled", 8, true},
		{"over cap is orphaned without reset", 9, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newCappedTestPool(8)
			b := &capBox{cap: tc.cap}
			r.Put(b)
			//: reset runs synchronously inside Put on the under-cap path only;
			//: the over-cap path returns before reset, leaving wasReset false.
			if b.wasReset != tc.wantReset {
				t.Errorf("wasReset = %v, want %v", b.wasReset, tc.wantReset)
			}
		})
	}
}
