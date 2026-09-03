// Package resilience — token-bucket rate-limit policy.
package resilience

import (
	"context"
	"math"
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
// Burst allowance), rejecting excess calls with RateLimited. A Rate that is not
// a finite positive number — zero, negative, NaN or infinite — is refused
// rather than defaulted: every call returns PolicyMisconfigured without running
// the operation (ADR 0031).
func NewRateLimiter(cfg RateLimiterConfig) coreres.Runner {
	//: a rate is the entire content of this policy — there is no SDK-side value
	//: that is not a guess at the caller's requirement, so refuse rather than
	//: invent one (ADR 0031). Without this, Rate 0 meant the bucket started
	//: full and never refilled: call one admitted, every call after it rejected
	//: forever with RATE_LIMITED, indistinguishable from working normally.
	//:
	//: The guard is finiteness, not just sign, because IEEE-754 lets two values
	//: through a bare `<= 0` and both rebuild the inert policy this refusal
	//: exists to prevent. NaN compares false against every bound, so it reaches
	//: the bucket, poisons `tokens` on the first refill (x + NaN is NaN) and
	//: makes `tokens >= 1` false forever: every call rejected with
	//: RATE_LIMITED, which is the zero-Rate defect exactly.
	//:
	//: +Inf is positive, so it reaches the bucket too, and it is the worse of
	//: the two because its outcome is not even stable. The refill term is
	//: `elapsed * rate`: at exactly zero elapsed time 0*(+Inf) is NaN and the
	//: call is rejected, at any non-zero elapsed it is +Inf and the call is
	//: admitted. The same limiter therefore rejects everything or admits
	//: everything according to clock granularity between two calls — and the
	//: admitting half fails OPEN, with the caller believing they are guarded.
	//:
	//: A rate arrives non-finite by ordinary arithmetic, not by a caller
	//: typing it: budget/window is +Inf for a zero window, and 0.0/0.0 is NaN.
	if math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) || cfg.Rate <= 0 {
		//: fail closed, and say why.
		return newMisconfigured("ratelimit", "Rate")
	}
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
