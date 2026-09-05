// Package resilience — the token-bucket internals.
package resilience

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_tokenBucket_take pins the refill arithmetic and the burst ceiling.
//
// The ceiling is what keeps a quiet period from becoming a thundering herd: an
// uncapped bucket would accumulate a token per interval forever, so a limiter
// idle for an hour would admit an hour's worth of calls in one instant — which
// is precisely the traffic shape it exists to prevent.
func Test_tokenBucket_take(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the bucket's starting credit and capacity.
		tokens float64
		burst  float64
		rate   float64
		//: how much time passes before the call.
		elapsed time.Duration
		want    bool
		//: the credit left afterwards.
		wantLeft float64
	}
	tests := []tc{
		{"a full bucket admits", 1, 1, 1, 0, true, 0},
		{"an empty bucket rejects", 0, 1, 1, 0, false, 0},
		{"a partial token rejects", 0.9, 1, 1, 0, false, 0.9},
		{"a second's refill at one per second admits", 0, 1, 1, time.Second, true, 0},
		{"a half-second's refill does not", 0, 1, 1, 500 * time.Millisecond, false, 0.5},
		{"a fast rate refills quickly", 0, 10, 100, 100 * time.Millisecond, true, 9},
		//: the ceiling: an hour idle still leaves only a burst's worth.
		{"a long idle period is capped at the burst", 0, 5, 1, time.Hour, true, 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &tokenBucket{
			clk:    clk,
			rate:   c.rate,
			burst:  c.burst,
			tokens: c.tokens,
			last:   clk.now,
		}
		clk.advance(c.elapsed)

		if got := b.take(); got != c.want {
			t.Fatalf("take() = %v, want %v (tokens now %v)", got, c.want, b.tokens)
		}
		//: floating-point credit, so compare within a small epsilon.
		if math.Abs(b.tokens-c.wantLeft) > 1e-9 {
			t.Errorf("tokens = %v, want %v", b.tokens, c.wantLeft)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_tokenBucket_Run pins the rejection, which must be typed and must not run
// the operation. A limiter that ran the call and then reported RATE_LIMITED
// would be worse than none at all.
func Test_tokenBucket_Run(t *testing.T) {
	t.Parallel()
	opErr := errors.New("the operation failed")

	type tc struct {
		name string
		//: the bucket's starting credit.
		tokens       float64
		opErr        error
		wantRejected bool
	}
	tests := []tc{
		{name: "credit admits", tokens: 1},
		{name: "an admitted operation's error propagates", tokens: 1, opErr: opErr},
		{name: "no credit rejects", tokens: 0, wantRejected: true},
		{name: "a partial token rejects", tokens: 0.5, wantRejected: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0)}
		b := &tokenBucket{clk: clk, rate: 1, burst: 1, tokens: c.tokens, last: clk.now}

		ran := false
		err := b.Run(t.Context(), func(context.Context) error {
			ran = true
			return c.opErr
		})

		if c.wantRejected {
			if !kerrs.HasCode(err, coreres.CodeRateLimited) {
				t.Fatalf("Run = %v, want RATE_LIMITED", err)
			}
			//: a rejected call must not have reached the dependency.
			if ran {
				t.Error("a rejected call still ran the operation")
			}
			return
		}
		if !ran {
			t.Fatal("an admitted call did not run the operation")
		}
		//: the operation's own error passes through untouched.
		if !errors.Is(err, c.opErr) {
			t.Errorf("Run = %v, want %v", err, c.opErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
