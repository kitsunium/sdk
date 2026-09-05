package resilience_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/resilience"
)

// TestComposition wraps Retry(Timeout(op)) through the public facade.
func TestComposition(t *testing.T) {
	t.Parallel()
	retry := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 2})
	timeout := resilience.NewTimeout(time.Second)
	//: a composed Retry(Timeout(op)) runs the op and returns nil.
	err := retry.Run(t.Context(), func(ctx context.Context) error {
		return timeout.Run(ctx, func(context.Context) error { return nil })
	})
	if err != nil {
		t.Fatalf("composition: %v", err)
	}
}
