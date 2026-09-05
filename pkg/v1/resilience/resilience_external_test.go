package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/resilience"
)

// TestComposition wraps Retry(Timeout(op)) through the public facade.
func TestComposition(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		attempts int
		failures int
		wantErr  bool
		wantRuns int
	}
	tests := []tc{
		{"an operation that succeeds first time", 2, 0, false, 1},
		{"one failure inside the retry budget", 2, 1, false, 2},
		{"more failures than the budget allows", 2, 3, true, 2},
		{"a single-attempt policy does not retry", 1, 1, true, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		retry := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: c.attempts})
		timeout := resilience.NewTimeout(time.Second)

		runs := 0
		//: the composition is the point: Retry drives Timeout, and the inner
		//: policy must not swallow the error the outer one retries on.
		err := retry.Run(t.Context(), func(ctx context.Context) error {
			return timeout.Run(ctx, func(context.Context) error {
				runs++
				if runs <= c.failures {
					return errBoom
				}
				return nil
			})
		})
		if (err != nil) != c.wantErr {
			t.Errorf("composition error = %v, want error = %v", err, c.wantErr)
		}
		if runs != c.wantRuns {
			t.Errorf("operation ran %d times, want %d", runs, c.wantRuns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// errBoom is the failure the composition test injects.
var errBoom = errors.New("boom")
