package clock_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

func TestSystem_Now(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		skewBudget time.Duration
	}{
		{"System.Now is within 1s of time.Now", time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := clock.System.Now()
			wall := time.Now()
			delta := wall.Sub(got)
			if delta < -tc.skewBudget || delta > tc.skewBudget {
				t.Errorf("System.Now diverges from time.Now by %v, budget %v", delta, tc.skewBudget)
			}
		})
	}
}

func TestSystem_Since(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wait time.Duration
	}{
		{"returns a non-negative duration after a tiny sleep", time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := clock.System.Now()
			time.Sleep(tc.wait)
			elapsed := clock.System.Since(start)
			if elapsed < 0 {
				t.Errorf("Since returned negative duration %v", elapsed)
			}
		})
	}
}

func TestSystem_NowIsMonotonicAcrossCalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"second call is not before first"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := clock.System.Now()
			second := clock.System.Now()
			if second.Before(first) {
				t.Errorf("System.Now went backwards: first=%v second=%v", first, second)
			}
		})
	}
}
