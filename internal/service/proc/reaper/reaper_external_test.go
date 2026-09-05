// Package reaper_test — the option surface as a caller uses it.
package reaper_test

import (
	"sync"
	"testing"

	svcreaper "github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// TestWithOnReap pins that the observer fires on EVERY sweep, including one that
// collected nothing.
//
// Reporting only non-empty sweeps would make the hook useless for the thing it
// is actually for: telling "the reaper is running and there is nothing to do"
// apart from "the reaper has stopped running". Those look identical from the
// outside, and only the first is fine.
func TestWithOnReap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		sweeps int
	}
	tests := []tc{
		{"a single sweep", 1},
		{"three sweeps", 3},
		{"ten sweeps", 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var mu sync.Mutex
		var calls int
		r := svcreaper.New(svcreaper.WithOnReap(func(int) {
			mu.Lock()
			calls++
			mu.Unlock()
		}))

		for range c.sweeps {
			//: an idle process reaps nothing, which is precisely the case the
			//: observer must still report.
			if _, err := r.ReapOnce(); err != nil {
				t.Fatalf("ReapOnce = %v, want nil", err)
			}
		}

		mu.Lock()
		defer mu.Unlock()
		if calls != c.sweeps {
			t.Errorf("the observer fired %d times for %d sweeps", calls, c.sweeps)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a nil observer must be accepted and simply do nothing, so a caller can
	//: pass one unconditionally.
	r := svcreaper.New(svcreaper.WithOnReap(nil))
	if _, err := r.ReapOnce(); err != nil {
		t.Errorf("ReapOnce with a nil observer = %v, want nil", err)
	}
}
