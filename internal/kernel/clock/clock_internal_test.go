package clock

import (
	"testing"
	"time"
)

func Test_systemClock_Now(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"returns a recent instant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := systemClock{}
			got := sc.Now()
			if got.IsZero() {
				t.Errorf("systemClock.Now returned zero time")
			}
		})
	}
}

func Test_systemClock_Since(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"returns non-negative elapsed duration from past instant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := systemClock{}
			past := time.Now().Add(-time.Millisecond)
			elapsed := sc.Since(past)
			if elapsed < 0 {
				t.Errorf("systemClock.Since(past) = %v, want >= 0", elapsed)
			}
		})
	}
}
