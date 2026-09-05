// Package resilience — the retry policy's internals.
package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_retryRunner_backoff pins the geometric growth and the ceiling.
//
// The cap is what keeps a retry policy from turning a brief outage into a
// multi-minute stall: without it the tenth attempt on a 100ms base at ×2 waits
// nearly a minute, long after the caller's own deadline has passed.
func Test_retryRunner_backoff(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		cfg     RetryConfig
		attempt int
		want    time.Duration
	}
	base := RetryConfig{BaseDelay: 100 * time.Millisecond, Multiplier: 2}
	capped := RetryConfig{BaseDelay: 100 * time.Millisecond, Multiplier: 2, MaxDelay: 250 * time.Millisecond}
	tests := []tc{
		{"the first retry waits the base delay", base, 1, 100 * time.Millisecond},
		{"the second doubles", base, 2, 200 * time.Millisecond},
		{"the third doubles again", base, 3, 400 * time.Millisecond},
		{"the fifth is sixteen times the base", base, 5, 1600 * time.Millisecond},
		{"the ceiling clamps growth", capped, 5, 250 * time.Millisecond},
		{"a delay below the ceiling is untouched", capped, 1, 100 * time.Millisecond},
		//: a tripled multiplier grows faster, which is the knob's whole point.
		{
			"a custom multiplier",
			RetryConfig{BaseDelay: 100 * time.Millisecond, Multiplier: 3},
			3, 900 * time.Millisecond,
		},
		{"a zero base delay stays zero", RetryConfig{Multiplier: 2}, 5, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &retryRunner{cfg: c.cfg}
		if got := r.backoff(c.attempt); got != c.want {
			t.Errorf("backoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_retryRunner_wait pins that the backoff is cancellable. A retry loop that
// slept unconditionally would keep a shutting-down process alive for its whole
// remaining budget, which on a capped exponential can be minutes.
func Test_retryRunner_wait(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		delay     time.Duration
		cancelled bool
	}
	tests := []tc{
		{name: "a short delay elapses", delay: time.Millisecond},
		{name: "a zero delay returns at once", delay: 0},
		{name: "a cancelled context aborts a long wait", delay: time.Hour, cancelled: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &retryRunner{cfg: RetryConfig{BaseDelay: c.delay, Multiplier: 1}}
		ctx := t.Context()
		if c.cancelled {
			stopped, cancel := context.WithCancel(t.Context())
			cancel()
			defer cancel()
			ctx = stopped
		}

		start := time.Now()
		err := r.wait(ctx, 1)

		if c.cancelled {
			//: the cancellation is surfaced verbatim so a caller shutting down
			//: recognises its own context error.
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("wait = %v, want the cancellation", err)
			}
			//: and it must not have slept the hour first.
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("the cancelled wait took %v", elapsed)
			}
			return
		}
		if err != nil {
			t.Fatalf("wait = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_retryRunner_Run pins the four ways the loop can end, and the one that is
// easy to get wrong.
//
// A DETERMINISTIC error — one the classifier rejects — must come back verbatim,
// immediately. Replaying it wastes the budget on something that cannot succeed,
// and relabelling it RETRY_EXHAUSTED hides the actual reason: a caller debugging
// a 400 Bad Request would see "retries exhausted" instead.
func Test_retryRunner_Run(t *testing.T) {
	t.Parallel()
	transient := errors.New("a transient failure")
	permanent := errors.New("a permanent failure")

	type tc struct {
		name string
		cfg  RetryConfig
		//: the error each attempt returns, in order; a nil entry succeeds.
		outcomes []error
		//: how many attempts must actually run.
		wantCalls int
		//: the sentinel the call must end with, or nil for success.
		wantCode kerrs.Code
		//: the raw error the call must return verbatim, if any.
		wantVerbatim error
	}
	fast := RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, Multiplier: 1}
	classified := RetryConfig{
		MaxAttempts: 3, BaseDelay: time.Millisecond, Multiplier: 1,
		Retryable: func(err error) bool { return errors.Is(err, transient) },
	}
	tests := []tc{
		{name: "a first-attempt success", cfg: fast, outcomes: []error{nil}, wantCalls: 1},
		{
			name:      "a success after two failures",
			cfg:       fast,
			outcomes:  []error{transient, transient, nil},
			wantCalls: 3,
		},
		{
			name:      "the budget runs out",
			cfg:       fast,
			outcomes:  []error{transient, transient, transient},
			wantCalls: 3,
			wantCode:  coreres.CodeRetryExhausted,
		},
		{
			//: the classifier rejects it, so the loop stops at once and hands
			//: the error back untouched.
			name:         "a deterministic failure is not replayed",
			cfg:          classified,
			outcomes:     []error{permanent},
			wantCalls:    1,
			wantVerbatim: permanent,
		},
		{
			name:         "a deterministic failure after a transient one",
			cfg:          classified,
			outcomes:     []error{transient, permanent},
			wantCalls:    2,
			wantVerbatim: permanent,
		},
		{
			name:      "a single-attempt budget",
			cfg:       RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, Multiplier: 1},
			outcomes:  []error{transient},
			wantCalls: 1,
			wantCode:  coreres.CodeRetryExhausted,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &retryRunner{cfg: c.cfg}
		calls := 0

		err := r.Run(t.Context(), func(context.Context) error {
			out := c.outcomes[min(calls, len(c.outcomes)-1)]
			calls++
			return out
		})

		if calls != c.wantCalls {
			t.Errorf("the operation ran %d times, want %d", calls, c.wantCalls)
		}
		switch {
		case c.wantVerbatim != nil:
			//: verbatim: the classifier's verdict must not be relabelled.
			if !errors.Is(err, c.wantVerbatim) {
				t.Fatalf("Run = %v, want %v verbatim", err, c.wantVerbatim)
			}
			if kerrs.HasCode(err, coreres.CodeRetryExhausted) {
				t.Error("a deterministic failure was relabelled RETRY_EXHAUSTED")
			}
		case c.wantCode != 0:
			if !kerrs.HasCode(err, c.wantCode) {
				t.Fatalf("Run = %v, want code %v", err, c.wantCode)
			}
		default:
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_retryRunner_RunCancelled pins that a cancelled context ends the loop with
// the CANCELLATION, not with RETRY_EXHAUSTED. A caller shutting down needs to
// recognise its own context error; being told the retries ran out would suggest
// the dependency is unhealthy when nothing was ever wrong with it.
func Test_retryRunner_RunCancelled(t *testing.T) {
	t.Parallel()
	transient := errors.New("a transient failure")

	type tc struct {
		name string
		//: whether the context is cancelled before Run, or during the first
		//: operation.
		before bool
	}
	tests := []tc{
		{"cancelled before the first attempt", true},
		{"cancelled during the first attempt", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &retryRunner{cfg: RetryConfig{MaxAttempts: 5, BaseDelay: time.Millisecond, Multiplier: 1}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if c.before {
			cancel()
		}

		err := r.Run(ctx, func(context.Context) error {
			if !c.before {
				cancel()
			}
			return transient
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want the cancellation", err)
		}
		if kerrs.HasCode(err, coreres.CodeRetryExhausted) {
			t.Error("a cancellation was relabelled RETRY_EXHAUSTED")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
