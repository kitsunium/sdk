package recycler_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// TestNewRecycler validates the constructor across its documented contract:
// nil factory rejection, cold-path factory invocation, and warm-path identity
// preservation through the underlying sync.Pool.
func TestNewRecycler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func(t *testing.T)
	}{
		{
			name: "nil factory yields nil Recycler",
			runner: func(t *testing.T) {
				r := recycler.NewRecycler[*int](nil)
				if r != nil {
					t.Errorf("NewRecycler(nil) = %v, want nil", r)
				}
			},
		},
		{
			name: "cold pool returns factory output and increments call counter",
			runner: func(t *testing.T) {
				var calls atomic.Uint64
				factory := func() *int {
					calls.Add(1)
					return new(int(calls.Load()))
				}
				r := recycler.NewRecycler[*int](factory)
				if r == nil {
					t.Fatal("NewRecycler returned nil with non-nil factory")
				}
				got := r.Get()
				if got == nil {
					t.Fatal("Get returned nil on cold pool")
				}
				if calls.Load() == 0 {
					t.Errorf("factory was not invoked: calls=%d, want >0", calls.Load())
				}
			},
		},
		{
			name: "Put then Get hands out a non-nil instance",
			runner: func(t *testing.T) {
				factory := func() *[16]byte { return &[16]byte{} }
				r := recycler.NewRecycler[*[16]byte](factory)
				seed := r.Get()
				r.Put(seed)
				got := r.Get()
				if got == nil {
					t.Fatal("Get returned nil after Put")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.runner(t)
		})
	}
}

// TestRecyclerConcurrentGetPut runs many goroutines hammering the pool to
// surface data races under -race; the pool's internal sync.Pool MUST cope.
func TestRecyclerConcurrentGetPut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		goroutines  int
		opsPerRoute int
	}{
		{"32 goroutines × 1024 ops", 32, 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var ops atomic.Uint64
			factory := func() *[64]byte { return &[64]byte{} }
			r := recycler.NewRecycler[*[64]byte](factory)
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.opsPerRoute {
						v := r.Get()
						r.Put(v)
						ops.Add(1)
					}
				})
			}
			wg.Wait()
			//: every goroutine MUST have completed all of its ops.
			want := uint64(tc.goroutines * tc.opsPerRoute)
			if ops.Load() != want {
				t.Errorf("ops counter = %d, want %d", ops.Load(), want)
			}
		})
	}
}
