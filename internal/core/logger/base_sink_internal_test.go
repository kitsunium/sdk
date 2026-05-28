package logger

import "testing"

// TestHasDuplicate exercises the unexported hasDuplicate helper. The
// scheme-validation path inside NewBaseSink depends on its correctness;
// this white-box test pins the contract so a future refactor that swaps
// the linear scan for a sort-based approach must still satisfy the same
// truth table.
func TestHasDuplicate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []string
		want bool
	}
	tests := []tc{
		{"nil slice has no duplicates", nil, false},
		{"single element has no duplicates", []string{"x"}, false},
		{"two distinct elements", []string{"a", "b"}, false},
		{"two identical elements", []string{"a", "a"}, true},
		{"three elements with trailing dup", []string{"a", "b", "a"}, true},
		{"three distinct elements", []string{"a", "b", "c"}, false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; hasDuplicate must return the documented truth value for each
	//: input shape.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := hasDuplicate(tc.in)
		//: the helper's return value must match the table expectation.
		if got != tc.want {
			t.Fatalf("hasDuplicate(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
