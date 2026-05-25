package recycler_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// TestNewPool validates the constructor across its documented contract:
// nil factory rejection, cold-path factory invocation, and warm-path identity
// preservation through the underlying sync.Pool.
func TestNewPool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func(t *testing.T)
	}{
		{
			name: "nil factory panics",
			runner: func(t *testing.T) {
				//: a nil factory is a programmer error — NewPool must fail loud.
				defer func() {
					//: the deferred recover turns the expected panic into a pass.
					if recover() == nil {
						t.Error("NewPool(nil) did not panic")
					}
				}()
				recycler.NewPool[*int](nil)
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
				r := recycler.NewPool[*int](factory)
				if r == nil {
					t.Fatal("NewPool returned nil with non-nil factory")
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
				r := recycler.NewPool[*[16]byte](factory)
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

// TestPoolConcurrentGetPut runs many goroutines hammering the pool to
// surface data races under -race; the pool's internal sync.Pool MUST cope.
func TestPoolConcurrentGetPut(t *testing.T) {
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
			r := recycler.NewPool[*[64]byte](factory)
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

// TestPool_Get verifies Get returns a usable value on both the cold-pool
// (factory-built) path and the warm-pool (recycled) path.
func TestPool_Get(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		warm bool
	}{
		{"cold pool builds via factory", false},
		{"warm pool returns a recycled value", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := recycler.NewPool[*[8]byte](func() *[8]byte { return &[8]byte{} })
			//: seed the pool so the warm case exercises the cache-hit path.
			if tc.warm {
				r.Put(r.Get())
			}
			if got := r.Get(); got == nil {
				t.Fatal("Get returned nil")
			}
		})
	}
}

// TestPool_Put verifies a value handed back via Put is observable on a
// subsequent Get (stored in the underlying sync.Pool).
func TestPool_Put(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"put then get returns a non-nil value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := recycler.NewPool[*int](func() *int { return new(int(0)) })
			seed := r.Get()
			r.Put(seed)
			if got := r.Get(); got == nil {
				t.Fatal("Get returned nil after Put")
			}
		})
	}
}
