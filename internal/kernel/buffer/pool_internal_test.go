package buffer

import "testing"

// Test_objectBucket_Get exercises both branches of objectBucket.Get: the happy
// path where the cached value type-asserts cleanly, and the defensive fallback
// where the underlying sync.Pool somehow returns a value of the wrong type
// (impossible in practice, but the branch must be covered).
func Test_objectBucket_Get(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func(t *testing.T)
	}{
		{
			name: "happy path returns the typed value from the underlying pool",
			runner: func(t *testing.T) {
				known := new(int(7))
				factory := func() *int { return new(int(0)) }
				bucket, ok := NewRecycler[*int](factory).(*objectBucket[*int])
				if !ok {
					t.Fatal("NewRecycler did not return *objectBucket[*int]")
				}
				//: override sync.Pool.New so Get deterministically receives the known instance.
				bucket.p.New = func() any { return known }
				got := bucket.Get()
				if got != known {
					t.Errorf("Get returned %p, want known %p", got, known)
				}
			},
		},
		{
			name: "wrong-type cache content falls back to factory",
			runner: func(t *testing.T) {
				var fallbackCalls int
				factory := func() *int {
					fallbackCalls++
					return new(int(42))
				}
				bucket, ok := NewRecycler[*int](factory).(*objectBucket[*int])
				if !ok {
					t.Fatal("NewRecycler did not return *objectBucket[*int]")
				}
				//: poison the underlying sync.Pool with the wrong concrete type.
				bucket.p.Put(new("not-a-pointer-to-int"))
				//: drain the factory invocation triggered by NewRecycler's first New call.
				fallbackCalls = 0
				got := bucket.Get()
				if got == nil {
					t.Fatal("Get returned nil after wrong-type fallback")
				}
				if fallbackCalls != 1 {
					t.Errorf("fallback factory calls = %d, want 1", fallbackCalls)
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

// Test_objectBucket_Put confirms that values handed back via Put are stored in
// the underlying sync.Pool and observable via a follow-up Get.
func Test_objectBucket_Put(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"put then get returns a non-nil value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			factory := func() *int { return new(int(0)) }
			bucket, ok := NewRecycler[*int](factory).(*objectBucket[*int])
			if !ok {
				t.Fatal("NewRecycler did not return *objectBucket[*int]")
			}
			seed := factory()
			bucket.Put(seed)
			got := bucket.Get()
			if got == nil {
				t.Fatal("Get returned nil after Put")
			}
		})
	}
}
