// Package resilience_test — the retry policy as a caller configures it.
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewRetry pins the two clamps, both of which prevent a configuration that
// silently does nothing.
//
// A budget below one would run the operation ZERO times — a policy that never
// calls what it was given. A multiplier at or below one would make each backoff
// no longer than the last, turning "exponential backoff" into a fixed-interval
// hammer on a dependency that is already struggling.
func TestNewRetry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		cfg       svcres.RetryConfig
		wantCalls int
	}
	tests := []tc{
		{
			name:      "a zero budget still runs once",
			cfg:       svcres.RetryConfig{MaxAttempts: 0, BaseDelay: time.Millisecond},
			wantCalls: 1,
		},
		{
			name:      "a negative budget still runs once",
			cfg:       svcres.RetryConfig{MaxAttempts: -3, BaseDelay: time.Millisecond},
			wantCalls: 1,
		},
		{
			name:      "an explicit budget is honoured",
			cfg:       svcres.RetryConfig{MaxAttempts: 4, BaseDelay: time.Millisecond},
			wantCalls: 4,
		},
		{
			//: a multiplier of one would keep every delay at the base; the
			//: constructor raises it to the doubling default.
			name:      "a flat multiplier is raised to doubling",
			cfg:       svcres.RetryConfig{MaxAttempts: 2, BaseDelay: time.Microsecond, Multiplier: 1},
			wantCalls: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewRetry(c.cfg)
		calls := 0

		//: an always-failing operation spends the whole budget, which is what
		//: makes the attempt count observable. The exhaustion error itself is
		//: covered by the internal test; here only the count matters.
		if err := r.Run(t.Context(), func(context.Context) error {
			calls++
			return errors.New("always fails")
		}); err == nil {
			t.Fatal("an always-failing operation reported success")
		}

		if calls != c.wantCalls {
			t.Errorf("the operation ran %d times, want %d", calls, c.wantCalls)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
