package resilience

import (
	"context"
	"math"
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// oneToken is the cost of a single admitted call.
const oneToken float64 = 1

// ratelimitPolicy names the limiter in a PolicyMisconfigured refusal.
const ratelimitPolicy string = "ratelimit"

// nanosPerSecond converts a wait in seconds to a time.Duration.
const nanosPerSecond float64 = 1e9

// tokenBucket admits a call when a token is available, refilling continuously at
// Rate tokens/sec up to Burst. With a positive maxWait, a call whose token is
// due within it reserves the token — tokens goes below zero, and the calls
// after it queue behind the debt — and waits for it on waiter; with none it is
// rejected at once.
type tokenBucket struct {
	mu      sync.Mutex
	clk     clock.Clock
	waiter  clock.Timed
	rate    float64
	burst   float64
	tokens  float64
	maxWait float64
	last    time.Time
}

// NewRateLimiter returns a Runner that admits at most Rate calls/sec (with a
// Burst allowance), rejecting excess calls with RateLimited. A Rate that is not
// a finite positive number — zero, negative, NaN or infinite — is refused
// rather than defaulted: every call returns PolicyMisconfigured without running
// the operation (ADR 0031).
//
// With a positive cfg.MaxWait, a call whose token is due within MaxWait
// reserves it and waits for it on cfg.Clock instead of being rejected; a
// call whose context ends while it waits returns ctx.Err() and hands its
// token back. A cfg.Clock that cannot wait — a clock.Clock that is not a
// clock.Timed — is refused when MaxWait is positive, naming Clock.
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
	if !usableRate(cfg.Rate) {
		//: fail closed, and say why.
		return newMisconfigured(ratelimitPolicy, "Rate")
	}
	//: default the clock to the system source.
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	//: one bucket, full, so the first call is admitted.
	bucket := newTokenBucket(clk, cfg.Rate, bucketSize(cfg.Burst))
	//: no wait asked for: reject at once, as the limiter always has.
	if cfg.MaxWait <= 0 {
		//: reject mode.
		return bucket
	}
	//: a wait is measured on the clock the bucket refills on, so that clock
	//: must be able to wait; a Now-only clock would split the two.
	waiter, timed := clk.(clock.Timed)
	//: refuse rather than wait on a second, different clock (ADR 0031).
	if !timed {
		//: fail closed, and say why.
		return newMisconfigured(ratelimitPolicy, "Clock")
	}
	bucket.waiter, bucket.maxWait = waiter, cfg.MaxWait.Seconds()
	//: wait mode.
	return bucket
}

// usableRate reports whether rate is a finite positive number of tokens per
// second — the only kind a bucket can refill at without failing open or
// closed. NewRateLimiter's comment says why each of the other shapes is fatal.
func usableRate(rate float64) bool {
	//: finiteness, not just sign: NaN and ±Inf both pass a bare `> 0` test
	//: or fail it for the wrong reason.
	return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate > 0
}

// bucketSize resolves a burst to the bucket capacity, clamping a non-positive
// one to a single token — the obvious floor: admit at least one call.
func bucketSize(burst int) float64 {
	//: a non-positive burst still allows a single token.
	if burst < 1 {
		//: minimum bucket of one token.
		return oneToken
	}
	//: the caller's capacity.
	return float64(burst)
}

// newTokenBucket returns a full bucket refilling at rate tokens per second up
// to burst, reading time from clk. It is the one constructor both the single
// limiter and every key of the keyed limiter go through.
func newTokenBucket(clk clock.Clock, rate, burst float64) *tokenBucket {
	//: start full so the first call is admitted.
	return &tokenBucket{clk: clk, rate: rate, burst: burst, tokens: burst, last: clk.Now()}
}

// Run admits op if a token is available — or due within maxWait, waited for
// on the clock — else rejects with RateLimited.
func (t *tokenBucket) Run(ctx context.Context, op coreres.Operation) error {
	admitted, wait := t.take()
	//: a refused token rejects the call fast, saying when the next one is due.
	if !admitted {
		//: bucket empty for this instant, and beyond the wait allowed.
		return rateLimited(wait)
	}
	//: a reserved token is waited for; an available one is not.
	if wait > 0 {
		//: the caller left while waiting: its token is handed back.
		if err := t.await(ctx, wait); err != nil {
			//: the caller's own verdict.
			return err
		}
	}
	//: token consumed — run the operation.
	return op(ctx)
}

// await waits waitSeconds on the bucket's clock for a reserved token. A
// context that ends first hands the token back and returns its error.
func (t *tokenBucket) await(ctx context.Context, waitSeconds float64) error {
	//: rounded up, so the call never runs before its token is earned.
	timer := t.waiter.NewTimer(time.Duration(math.Ceil(waitSeconds * nanosPerSecond)))
	//: always release the timer.
	defer timer.Stop()
	select {
	//: the token is due.
	case <-timer.C():
		//: run.
		return nil
	//: the caller left first.
	case <-ctx.Done():
		t.giveBack()
		//: the cancellation, not a rejection: the limiter refused nothing.
		return ctx.Err()
	}
}

// giveBack returns a reserved token a caller did not use. Holds mu.
func (t *tokenBucket) giveBack() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refill()
	//: never past the bucket's capacity.
	t.tokens = min(t.tokens+oneToken, t.burst)
}

// rateLimited is the RateLimited rejection carrying, as retryAfterField, the
// seconds the bucket needs to earn its next token — the delay RetryAfter reads.
func rateLimited(waitSeconds float64) error {
	//: the sentinel stays the origin; the delay travels as a field.
	return wrapAsFields(coreres.RateLimited, kerrs.Float(retryAfterField, waitSeconds))
}

// take refills by elapsed time then consumes one token if available. Holds mu.
// A token that is not available yet is reserved when it is due within
// maxWait: the call is admitted with how many seconds it must wait for it.
// A refusal reports how many seconds the bucket needs, at its rate, to hold a
// whole token again: (1 − tokens) / rate.
func (t *tokenBucket) take() (admitted bool, waitSeconds float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refill()
	//: a full token admits the call.
	if t.tokens >= oneToken {
		//: consume it.
		t.tokens -= oneToken
		//: admitted.
		return true, 0
	}
	//: not enough credit this instant; the rest of a token, earned at rate.
	waitSeconds = (oneToken - t.tokens) / t.rate
	//: due soon enough: reserve it, so the calls after this one queue behind.
	if waitSeconds <= t.maxWait {
		t.tokens -= oneToken
		//: admitted, after the wait.
		return true, waitSeconds
	}
	//: rejected, saying when the token is due.
	return false, waitSeconds
}

// refill adds the tokens earned since the last reading, never past the
// bucket's capacity. The caller holds mu.
func (t *tokenBucket) refill() {
	//: refill proportional to the elapsed time since the last call.
	now := t.clk.Now()
	t.tokens += now.Sub(t.last).Seconds() * t.rate
	t.last = now
	//: never accumulate beyond the burst capacity.
	if t.tokens > t.burst {
		//: cap at the bucket size.
		t.tokens = t.burst
	}
}
