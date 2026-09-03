package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	res "github.com/kitsunium/sdk/internal/service/resilience"
)

// fakeClock is a settable clock for deterministic breaker/limiter tests.
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time                  { return f.now }
func (f *fakeClock) Since(t time.Time) time.Duration { return f.now.Sub(t) }
func (f *fakeClock) advance(d time.Duration)         { f.now = f.now.Add(d) }

var errBoom = errors.New("boom")

// TestRetrySucceedsThenExhausts covers the two retry outcomes.
func TestRetrySucceedsThenExhausts(t *testing.T) {
	t.Parallel()
	r := res.NewRetry(res.RetryConfig{MaxAttempts: 3})
	//: an op that succeeds on the 3rd attempt returns nil.
	calls := 0
	err := r.Run(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errBoom
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry-success: err=%v calls=%d, want nil/3", err, calls)
	}
	//: an always-failing op exhausts and wraps as RetryExhausted.
	err = r.Run(context.Background(), func(context.Context) error { return errBoom })
	if !errs.HasCode(err, coreres.CodeRetryExhausted) {
		t.Errorf("retry-exhaust: err=%v, want RETRY_EXHAUSTED", err)
	}
}

// TestTimeout covers the deadline path + the fast path.
func TestTimeout(t *testing.T) {
	t.Parallel()
	to := res.NewTimeout(10 * time.Millisecond)
	//: an op that respects ctx and overruns surfaces TIMEOUT_EXCEEDED.
	err := to.Run(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errs.HasCode(err, coreres.CodeTimeoutExceeded) {
		t.Errorf("timeout: err=%v, want TIMEOUT_EXCEEDED", err)
	}
	//: a fast op passes through.
	if err := to.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("timeout fast-path: err=%v, want nil", err)
	}
}

// TestBreakerTripsAndRecovers covers Closed→Open→HalfOpen→Closed.
func TestBreakerTripsAndRecovers(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	b := res.NewCircuitBreaker(res.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute, Clock: clk})
	fail := func(context.Context) error { return errBoom }
	//: two failures trip the breaker Open.
	b.Run(context.Background(), fail)
	b.Run(context.Background(), fail)
	//: a call while Open is rejected fast with CIRCUIT_OPEN.
	if err := b.Run(context.Background(), fail); !errs.HasCode(err, coreres.CodeCircuitOpen) {
		t.Fatalf("breaker open: err=%v, want CIRCUIT_OPEN", err)
	}
	//: after the cooldown, a successful trial closes the breaker.
	clk.advance(2 * time.Minute)
	if err := b.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("breaker half-open trial: err=%v, want nil", err)
	}
	//: the breaker is Closed again and admits calls.
	if err := b.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("breaker closed: err=%v, want nil", err)
	}
}

// TestRateLimiterBurst covers burst admission then rejection.
func TestRateLimiterBurst(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	rl := res.NewRateLimiter(res.RateLimiterConfig{Rate: 1, Burst: 2, Clock: clk})
	noop := func(context.Context) error { return nil }
	//: the burst of 2 is admitted.
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Fatalf("token 1: %v", err)
	}
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Fatalf("token 2: %v", err)
	}
	//: the 3rd immediate call is rate-limited.
	if err := rl.Run(context.Background(), noop); !errs.HasCode(err, coreres.CodeRateLimited) {
		t.Fatalf("token 3: err=%v, want RATE_LIMITED", err)
	}
	//: after 1s a refill (rate=1/s) admits one more.
	clk.advance(time.Second)
	if err := rl.Run(context.Background(), noop); err != nil {
		t.Errorf("after refill: err=%v, want nil", err)
	}
}

// TestBulkheadRejectsWhenFull uses a re-entrant Run (no goroutine): the outer
// op holds the only slot, so a nested Run sees the bulkhead full.
func TestBulkheadRejectsWhenFull(t *testing.T) {
	t.Parallel()
	bh := res.NewBulkhead(1)
	var nestedErr error
	//: the outer Run occupies the single slot for the duration of its op.
	outerErr := bh.Run(context.Background(), func(ctx context.Context) error {
		//: a nested Run, while the slot is held, must be rejected.
		nestedErr = bh.Run(ctx, func(context.Context) error { return nil })
		//: the outer op itself succeeds.
		return nil
	})
	//: the outer op completed cleanly.
	if outerErr != nil {
		t.Fatalf("outer Run: %v", outerErr)
	}
	//: the nested call hit the full bulkhead.
	if !errs.HasCode(nestedErr, coreres.CodeBulkheadFull) {
		t.Errorf("nested Run: err=%v, want BULKHEAD_FULL", nestedErr)
	}
}

// errDeny stands for a deterministic refusal (policy denial, HTTP 400): replaying
// it cannot change the outcome.
var errDeny = errors.New("deny")

// notDeny is the classifier the tests share: everything is transient except the
// deterministic refusal.
func notDeny(err error) bool { return !errors.Is(err, errDeny) }

// TestRetryRetryablePredicate pins the classifier contract: a rejected error is
// returned verbatim on the first attempt — no backoff, no budget spent, no
// RetryExhausted relabel — while a nil predicate keeps replaying everything.
func TestRetryRetryablePredicate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name         string
		retryable    func(error) bool
		opErrs       []error
		baseDelay    time.Duration
		wantCalls    int
		wantVerbatim error
	}
	tests := []tc{
		{
			//: no classifier — the pre-classifier behaviour, budget fully spent.
			"nil predicate exhausts the budget",
			nil,
			[]error{errBoom, errBoom, errBoom},
			0,
			3,
			nil,
		},
		{
			//: an accepting classifier is indistinguishable from no classifier.
			"accepting predicate exhausts the budget",
			notDeny,
			[]error{errBoom, errBoom, errBoom},
			0,
			3,
			nil,
		},
		{
			//: the defect case — a deterministic error must cost one attempt.
			"rejecting predicate returns on the first attempt",
			notDeny,
			[]error{errDeny, errDeny, errDeny},
			time.Second,
			1,
			errDeny,
		},
		{
			//: the mixed sequence — transient failures replay, then a refusal stops.
			"rejecting predicate stops mid-budget",
			notDeny,
			[]error{errBoom, errBoom, errDeny},
			0,
			3,
			errDeny,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the first-attempt case carries a one-second backoff: the fix never
		//: sleeps it, so a regression shows up as elapsed time, not a hung test.
		r := res.NewRetry(res.RetryConfig{MaxAttempts: 3, BaseDelay: tc.baseDelay, Retryable: tc.retryable})
		calls := 0
		start := time.Now()
		//: the op walks the scripted error sequence, one entry per attempt.
		err := r.Run(t.Context(), func(context.Context) error {
			calls++
			return tc.opErrs[calls-1]
		})
		//: the classifier decides how much of the budget the failure costs.
		if calls != tc.wantCalls {
			t.Fatalf("calls=%d, want %d", calls, tc.wantCalls)
		}
		//: a transient-only run still exhausts and wraps as RETRY_EXHAUSTED.
		if tc.wantVerbatim == nil {
			if !errs.HasCode(err, coreres.CodeRetryExhausted) {
				t.Errorf("err=%v, want RETRY_EXHAUSTED", err)
			}
			return
		}
		//: a rejected error surfaces as itself, never behind RETRY_EXHAUSTED.
		if !errors.Is(err, tc.wantVerbatim) {
			t.Errorf("err=%v, want %v verbatim", err, tc.wantVerbatim)
		}
		if errs.HasCode(err, coreres.CodeRetryExhausted) {
			t.Errorf("err=%v was relabelled RETRY_EXHAUSTED", err)
		}
		//: stopping early also means the pending backoff was never slept (the
		//: configured BaseDelay is 1s in that case, so 250ms leaves a 4x margin).
		if elapsed := time.Since(start); tc.baseDelay > 0 && elapsed > 250*time.Millisecond {
			t.Errorf("elapsed=%v, want an immediate return (no backoff)", elapsed)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestBreakerRetryablePredicate pins the breaker half of the classifier: a
// deterministic error is neither a failure nor a success, so it can neither trip
// a Closed breaker nor close a HalfOpen one.
func TestBreakerRetryablePredicate(t *testing.T) {
	t.Parallel()
	//: nil means "the op succeeds"; anything else is the error it returns.
	type step struct {
		opErr    error
		advance  time.Duration
		wantCode bool
		wantErr  error
	}
	type tc struct {
		name      string
		retryable func(error) bool
		steps     []step
	}
	tests := []tc{
		{
			//: no classifier — every error counts, so refusals trip the breaker.
			"nil predicate lets a deterministic error trip the breaker",
			nil,
			[]step{
				{errDeny, 0, false, errDeny},
				{errDeny, 0, false, errDeny},
				//: threshold reached: the 3rd call is rejected fast.
				{errDeny, 0, true, nil},
			},
		},
		{
			//: the fix — refusals never reach the state machine, so it stays Closed.
			"rejecting predicate keeps the breaker closed",
			notDeny,
			[]step{
				{errDeny, 0, false, errDeny},
				{errDeny, 0, false, errDeny},
				{errDeny, 0, false, errDeny},
				//: still Closed, so a real call is admitted and succeeds.
				{nil, 0, false, nil},
			},
		},
		{
			//: a refusal during a HalfOpen trial is no verdict — stay HalfOpen.
			"rejecting predicate does not close a half-open breaker",
			notDeny,
			[]step{
				//: one transient failure trips the breaker (threshold 2 below is
				//: reached on the second).
				{errBoom, 0, false, errBoom},
				{errBoom, 0, false, errBoom},
				//: Open within the cooldown: rejected fast.
				{nil, 0, true, nil},
				//: the cooldown elapses, admitting a HalfOpen trial…
				{errDeny, 2 * time.Minute, false, errDeny},
				//: …which decided nothing, so the next trial is still admitted.
				{nil, 0, false, nil},
				//: the success closed the breaker; a fresh failure counts from zero.
				{errBoom, 0, false, errBoom},
			},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		clk := &fakeClock{now: time.Unix(0, 0)}
		b := res.NewCircuitBreaker(res.BreakerConfig{
			FailureThreshold: 2, OpenDuration: time.Minute, Clock: clk, Retryable: tc.retryable,
		})
		for i, st := range tc.steps {
			//: move the injected clock before the call when the step says so.
			clk.advance(st.advance)
			err := b.Run(t.Context(), func(context.Context) error { return st.opErr })
			//: a rejected call carries the policy sentinel instead of an outcome.
			if got := errs.HasCode(err, coreres.CodeCircuitOpen); got != st.wantCode {
				t.Fatalf("step %d: CIRCUIT_OPEN=%v, want %v (err=%v)", i, got, st.wantCode, err)
			}
			//: an admitted call propagates the operation's own outcome verbatim.
			if !st.wantCode && !errors.Is(err, st.wantErr) {
				t.Fatalf("step %d: err=%v, want %v verbatim", i, err, st.wantErr)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestBreakerOpenDurationClamp is the regression guard for ADR 0031: a breaker
// built without an OpenDuration must still reject calls once it trips.
//
// OpenDuration was the one knob in BreakerConfig with no lower bound, so a
// caller who set only FailureThreshold got a breaker whose cooldown was zero:
// it tripped, then admitted the very next call, and every call after it. It
// rejected nothing while the caller believed it was protected. The guard
// asserts the observable outcome — a call is actually rejected — not the field
// value, so it fails whatever the clamp looks like internally.
func TestBreakerOpenDurationClamp(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  res.BreakerConfig
		//: how far the clock moves before the post-trip probe.
		advance time.Duration
		//: whether that probe must be rejected with CIRCUIT_OPEN.
		wantOpen bool
	}
	tests := []tc{
		{
			//: the defect case — no OpenDuration at all.
			"zero OpenDuration still rejects after tripping",
			res.BreakerConfig{FailureThreshold: 2},
			0,
			true,
		},
		{
			//: the clamped cooldown must actually elapse before recovery.
			"zero OpenDuration recovers only after the default cooldown",
			res.BreakerConfig{FailureThreshold: 2},
			31 * time.Second,
			false,
		},
		{
			//: just under the default cooldown is still Open.
			"zero OpenDuration is still open just before the default cooldown",
			res.BreakerConfig{FailureThreshold: 2},
			29 * time.Second,
			true,
		},
		{
			//: an explicit cooldown is honoured, not overridden by the clamp.
			"explicit OpenDuration is honoured",
			res.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute},
			31 * time.Second,
			true,
		},
		{
			//: and it recovers on its own schedule, not the default's.
			"explicit OpenDuration recovers on its own schedule",
			res.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute},
			61 * time.Second,
			false,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		clk := &fakeClock{now: time.Unix(0, 0)}
		cfg := tc.cfg
		cfg.Clock = clk
		b := res.NewCircuitBreaker(cfg)
		fail := func(context.Context) error { return errBoom }
		//: spend the failure budget so the breaker trips Open. Every call in
		//: this phase must still run the operation and surface its own error —
		//: a rejection here would mean the breaker tripped early.
		for range cfg.FailureThreshold {
			if err := b.Run(t.Context(), fail); !errors.Is(err, errBoom) {
				t.Fatalf("trip phase: err=%v, want errBoom", err)
			}
		}
		//: move the clock to the moment under test.
		clk.advance(tc.advance)
		//: probe with an operation that would succeed if it were admitted; a
		//: rejected probe never runs it, which is what CIRCUIT_OPEN reports.
		ran := false
		err := b.Run(t.Context(), func(context.Context) error { ran = true; return nil })
		//: the observable contract: rejected fast, and the op did not execute.
		if got := errs.HasCode(err, coreres.CodeCircuitOpen); got != tc.wantOpen {
			t.Fatalf("CIRCUIT_OPEN=%v, want %v (err=%v)", got, tc.wantOpen, err)
		}
		//: a rejection that still ran the operation would protect nothing.
		if ran == tc.wantOpen {
			t.Errorf("operation ran=%v while CIRCUIT_OPEN=%v — the two must be opposite", ran, tc.wantOpen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestRateLimiterRejectsAZeroRate is the regression guard for the refusal arm of
// ADR 0031.
//
// A zero Rate made the token bucket start full and never refill: call one was
// admitted, and every call after it was rejected with RATE_LIMITED forever. The
// policy could never do its job, and said so with the same error it uses when
// it IS doing its job — a misconfiguration presented as normal operation. The
// guard asserts the observable outcome: the operation does not run, and the
// error is distinguishable from an ordinary rate-limit rejection.
func TestRateLimiterRejectsAZeroRate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  res.RateLimiterConfig
		//: whether the policy must refuse itself rather than operate.
		wantMisconfigured bool
	}
	tests := []tc{
		{
			//: the defect case — no Rate at all.
			"zero Rate is refused",
			res.RateLimiterConfig{},
			true,
		},
		{
			//: a Burst without a Rate is still a policy that cannot work.
			"zero Rate with a Burst is refused",
			res.RateLimiterConfig{Burst: 10},
			true,
		},
		{
			//: a negative Rate is no more honourable than a zero one.
			"negative Rate is refused",
			res.RateLimiterConfig{Rate: -1},
			true,
		},
		{
			//: a real rate still builds a working limiter.
			"positive Rate operates normally",
			res.RateLimiterConfig{Rate: 1, Burst: 1},
			false,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		cfg := tc.cfg
		cfg.Clock = &fakeClock{now: time.Unix(0, 0)}
		rl := res.NewRateLimiter(cfg)
		ran := false
		err := rl.Run(t.Context(), func(context.Context) error { ran = true; return nil })
		//: a refused policy never runs the operation.
		if ran == tc.wantMisconfigured {
			t.Fatalf("operation ran=%v while misconfigured=%v — the two must be opposite", ran, tc.wantMisconfigured)
		}
		//: and the refusal must be POLICY_MISCONFIGURED, never RATE_LIMITED —
		//: telling a broken policy apart from a working one is the whole point.
		if got := errs.HasCode(err, coreres.CodePolicyMisconfigured); got != tc.wantMisconfigured {
			t.Fatalf("POLICY_MISCONFIGURED=%v, want %v (err=%v)", got, tc.wantMisconfigured, err)
		}
		//: a misconfiguration is permanent, so it must not masquerade as the
		//: transient outcome a caller might reasonably retry.
		if tc.wantMisconfigured && errs.HasCode(err, coreres.CodeRateLimited) {
			t.Errorf("err=%v also carries RATE_LIMITED — a misconfiguration is not a transient rejection", err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestTimeoutRejectsANonPositiveDuration is the second refusal arm of ADR 0031.
//
// context.WithTimeout(ctx, 0) hands back an already-expired context, so
// NewTimeout(0) failed every operation with TimeoutExceeded — including one
// that returns instantly. Like the zero-Rate limiter it could never admit
// anything, and it said so with the error it uses when it IS working, so a
// caller reading TIMEOUT_EXCEEDED had no way to tell a slow dependency from a
// policy that was never viable.
func TestTimeoutRejectsANonPositiveDuration(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		d    time.Duration
		//: whether the policy must refuse itself rather than operate.
		wantMisconfigured bool
	}
	tests := []tc{
		{"zero duration is refused", 0, true},
		{"negative duration is refused", -time.Second, true},
		{"positive duration operates normally", time.Minute, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		ran := false
		//: an operation that returns instantly would succeed under any honest
		//: deadline, so anything but success here is the policy's own doing.
		err := res.NewTimeout(tc.d).Run(t.Context(), func(context.Context) error { ran = true; return nil })
		//: a refused policy never runs the operation.
		if ran == tc.wantMisconfigured {
			t.Fatalf("operation ran=%v while misconfigured=%v — the two must be opposite", ran, tc.wantMisconfigured)
		}
		//: the refusal must be POLICY_MISCONFIGURED, never TIMEOUT_EXCEEDED.
		if got := errs.HasCode(err, coreres.CodePolicyMisconfigured); got != tc.wantMisconfigured {
			t.Fatalf("POLICY_MISCONFIGURED=%v, want %v (err=%v)", got, tc.wantMisconfigured, err)
		}
		//: a misconfiguration must not masquerade as the transient outcome.
		if tc.wantMisconfigured && errs.HasCode(err, coreres.CodeTimeoutExceeded) {
			t.Errorf("err=%v also carries TIMEOUT_EXCEEDED — a misconfiguration is not a deadline overrun", err)
		}
		//: and a viable deadline still lets an instant operation through.
		if !tc.wantMisconfigured && err != nil {
			t.Errorf("err=%v, want nil", err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
