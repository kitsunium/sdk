package recycler

import "testing"

// Test_Pool_Get_failLoud covers the white-box assertion path in Get: the
// pool is contractually homogeneous (only ever holds T), so a wrong-typed
// entry is an internal invariant break that MUST panic rather than silently
// degrade. Reaching it requires poisoning the unexported pool, hence a
// white-box test.
func Test_Pool_Get_failLoud(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		runner    func(t *testing.T)
		wantPanic bool
	}
	tests := []tc{
		{
			name: "homogeneous pool returns the typed value",
			runner: func(t *testing.T) {
				r := NewPool[*int](func() *int { return new(int(0)) })
				if got := r.Get(); got == nil {
					t.Fatal("Get returned nil on a healthy pool")
				}
			},
			wantPanic: false,
		},
		{
			name: "wrong-typed pool content fails loud",
			runner: func(t *testing.T) {
				r := NewPool[*int](func() *int { return new(int(0)) })
				//: poison the factory so the next cache miss yields a non-*int.
				r.pool.New = func() any { return "not-a-pointer-to-int" }
				//: empty pool → Get triggers New → the assertion in Get must panic.
				r.Get()
			},
			wantPanic: true,
		},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the deferred recover turns a panic into an assertable bool.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		defer func() {
			//: recover so a panic becomes an assertable outcome.
			got := recover() != nil
			//: the recovered state must match the case's expectation.
			if got != tc.wantPanic {
				t.Errorf("panic = %v, want %v", got, tc.wantPanic)
			}
		}()
		tc.runner(t)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
