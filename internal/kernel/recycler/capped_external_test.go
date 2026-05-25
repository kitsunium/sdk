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
	type tc struct {
		name      string
		runner    func()
		wantPanic bool
	}
	tests := []tc{
		{"nil reset panics", func() { recycler.NewCappedPool[*capBox](mk, nil, func(*capBox) int { return 0 }, 8) }, true},
		{"nil capOf panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, nil, 8) }, true},
		{"zero maxCap panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, func(*capBox) int { return 0 }, 0) }, true},
		{"negative maxCap panics", func() { recycler.NewCappedPool[*capBox](mk, func(*capBox) {}, func(*capBox) int { return 0 }, -1) }, true},
		{"valid configuration does not panic", func() { newCappedTestPool(8) }, false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the deferred recover matches the case's panic expectation.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		defer func() {
			//: capture the recovered state once and match the expectation.
			got := recover() != nil
			if got != tc.wantPanic {
				t.Errorf("panic = %v, want %v", got, tc.wantPanic)
			}
		}()
		tc.runner()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestCappedPool_Get verifies Get hands out a usable value on a cold pool.
func TestCappedPool_Get(t *testing.T) {
	t.Parallel()
	type tc struct{ name string }
	tests := []tc{
		{"cold pool builds via factory"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; a cold pool must still hand out a non-nil value.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		r := newCappedTestPool(8)
		if got := r.Get(); got == nil {
			t.Fatal("Get returned nil on cold pool")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestCappedPool_Put verifies the discard-before-reset policy: a value at
// or under maxCap is reset (and repooled); an over-cap value is orphaned
// WITHOUT reset (so a caller still aliasing its bytes is not clobbered).
func TestCappedPool_Put(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		cap       int
		wantReset bool
	}
	tests := []tc{
		{"under cap is reset and repooled", 4, true},
		{"at cap is reset and repooled", 8, true},
		{"over cap is orphaned without reset", 9, false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; reset runs inside Put on the under-cap path only.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		r := newCappedTestPool(8)
		b := &capBox{cap: tc.cap}
		r.Put(b)
		//: the over-cap path returns before reset, leaving wasReset false.
		if b.wasReset != tc.wantReset {
			t.Errorf("wasReset = %v, want %v", b.wasReset, tc.wantReset)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
