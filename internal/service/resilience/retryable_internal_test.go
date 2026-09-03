package resilience

import (
	"errors"
	"testing"
)

// Test_isRetryable pins the nil-predicate default: without a classifier every
// error stays transient, which is the contract the policies had before the
// classifier existed.
func Test_isRetryable(t *testing.T) {
	t.Parallel()
	errDeterministic := errors.New("bad request")
	type tc struct {
		name string
		pred func(error) bool
		err  error
		want bool
	}
	tests := []tc{
		{
			//: no classifier — the historic "replay everything" default.
			"nil predicate treats any error as transient",
			nil,
			errDeterministic,
			true,
		},
		{
			//: an accepting classifier keeps the historic behaviour explicitly.
			"predicate accepting the error",
			func(error) bool { return true },
			errDeterministic,
			true,
		},
		{
			//: a rejecting classifier marks the error deterministic.
			"predicate rejecting the error",
			func(error) bool { return false },
			errDeterministic,
			false,
		},
		{
			//: the predicate receives the very error the policy observed.
			"predicate sees the caller's error",
			func(err error) bool { return !errors.Is(err, errDeterministic) },
			errDeterministic,
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the helper is the single place the nil default is applied.
			if got := isRetryable(tc.pred, tc.err); got != tc.want {
				t.Errorf("isRetryable = %v, want %v", got, tc.want)
			}
		})
	}
}
