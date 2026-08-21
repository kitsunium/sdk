// Package resilience — token-bucket rate-limit policy.
package resilience

import (
	"context"
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// oneToken is the cost of a single admitted call.
const oneToken float64 = 1

// tokenBucket admits a call when a token is available, refilling continuously at
// Rate tokens/sec up to Burst. Reject mode — no waiting.
type tokenBucket struct {
	mu     sync.Mutex
	clk    clock.Clock
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a Runner that admits at most Rate calls/sec (with a
// Burst allowance), rejecting excess calls with RateLimited.
func NewRateLimiter(cfg RateLimiterConfig) coreres.Runner {
	//: default the clock to the system source.
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	//: a non-positive burst still allows a single token.
	burst := float64(cfg.Burst)
	//: clamp a non-positive burst to one token.
	if cfg.Burst < 1 {
		//: minimum bucket of one token.
		burst = oneToken
	}
	//: start full so the first call is admitted.
	return &tokenBucket{clk: clk, rate: cfg.Rate, burst: burst, tokens: burst, last: clk.Now()}
}

// Run admits op if a token is available, else rejects with RateLimited.
func (t *tokenBucket) Run(ctx context.Context, op coreres.Operation) error {
	//: a refused token rejects the call fast.
	if !t.take() {
		//: bucket empty for this instant.
		return wrapAs(coreres.RateLimited, nil)
	}
	//: token consumed — run the operation.
	return op(ctx)
}

// take refills by elapsed time then consumes one token if available. Holds mu.
func (t *tokenBucket) take() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	//: refill proportional to the elapsed time since the last call.
	now := t.clk.Now()
	t.tokens += now.Sub(t.last).Seconds() * t.rate
	t.last = now
	//: never accumulate beyond the burst capacity.
	if t.tokens > t.burst {
		//: cap at the bucket size.
		t.tokens = t.burst
	}
	//: a full token admits the call.
	if t.tokens >= oneToken {
		//: consume it.
		t.tokens -= oneToken
		//: admitted.
		return true
	}
	//: not enough credit this instant.
	return false
}
