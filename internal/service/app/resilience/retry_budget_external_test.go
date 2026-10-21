// Package resilience_test — the retry budget, alone and shared by a retry and
// a hedge.
package resilience_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// errDown is the retryable failure the budget tests replay.
var errDown = errors.New("down")

// spyBudget is a RetryBudget a test reads back: it answers AllowRetry with
// allow, signals each question on asked when set, and counts the records.
type spyBudget struct {
	allow     atomic.Bool
	asked     chan struct{}
	questions atomic.Int32
	successes atomic.Int32
	failures  atomic.Int32
}

// AllowRetry implements coreres.RetryBudget.
func (s *spyBudget) AllowRetry() bool {
	s.questions.Add(1)
	if s.asked != nil {
		s.asked <- struct{}{}
	}
	return s.allow.Load()
}

// RecordSuccess implements coreres.RetryBudget.
func (s *spyBudget) RecordSuccess() { s.successes.Add(1) }

// RecordFailure implements coreres.RetryBudget.
func (s *spyBudget) RecordFailure() { s.failures.Add(1) }

// newSpy returns a spy that allows retries, or not.
func newSpy(allow bool) *spyBudget {
	s := &spyBudget{}
	s.allow.Store(allow)
	return s
}

// TestNewRetryBudget_Refusals pins ADR 0031 on the budget: an unusable
// MaxTokens or TokenRatio builds a budget that allows nothing, and a retry or
// a hedge given it refuses every call by name without running the operation.
func TestNewRetryBudget_Refusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  svcres.RetryBudgetConfig
		knob string
	}
	tests := []tc{
		{name: "zero MaxTokens", cfg: svcres.RetryBudgetConfig{TokenRatio: 0.1}, knob: "MaxTokens"},
		{name: "negative MaxTokens", cfg: svcres.RetryBudgetConfig{MaxTokens: -1, TokenRatio: 0.1}, knob: "MaxTokens"},
		{name: "zero TokenRatio", cfg: svcres.RetryBudgetConfig{MaxTokens: 10}, knob: "TokenRatio"},
		{name: "a ratio below a thousandth", cfg: svcres.RetryBudgetConfig{MaxTokens: 10, TokenRatio: 0.0004}, knob: "TokenRatio"},
		{name: "a negative ratio", cfg: svcres.RetryBudgetConfig{MaxTokens: 10, TokenRatio: -1}, knob: "TokenRatio"},
		{name: "a NaN ratio", cfg: svcres.RetryBudgetConfig{MaxTokens: 10, TokenRatio: math.NaN()}, knob: "TokenRatio"},
		{name: "an infinite ratio", cfg: svcres.RetryBudgetConfig{MaxTokens: 10, TokenRatio: math.Inf(1)}, knob: "TokenRatio"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			budget := svcres.NewRetryBudget(c.cfg)
			if budget.AllowRetry() {
				t.Error("a refused budget allowed a retry")
			}
			runners := map[string]coreres.Runner{
				"retry": svcres.NewRetry(svcres.RetryConfig{MaxAttempts: 3, Budget: budget}),
				"hedge": svcres.NewHedge(svcres.HedgeConfig{
					Idempotent: true, Delay: time.Second, MaxInFlight: 1, Budget: budget,
				}),
			}
			for name, r := range runners {
				ran := false
				err := r.Run(t.Context(), func(context.Context) error {
					ran = true
					return nil
				})
				if ran || !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
					t.Fatalf("%s: ran=%v err=%v, want POLICY_MISCONFIGURED without running", name, ran, err)
				}
				if !hasField(err, "knob", c.knob) {
					t.Errorf("%s: %v does not name the knob %s", name, err, c.knob)
				}
			}
		})
	}
}

// hasField reports whether err carries the field key with the rendering want.
func hasField(err error, key, want string) bool {
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == key && field.StringValue() == want {
			return true
		}
	}
	return false
}

// TestRetryBudget_TheBucket pins gRFC A6's arithmetic on a ten-token bucket
// with a ratio of 0.1: full at the start, a failure costs one, a success puts
// back a tenth, never past the top, and retries are allowed only while MORE
// than half the tokens remain.
func TestRetryBudget_TheBucket(t *testing.T) {
	t.Parallel()
	b := svcres.NewRetryBudget(svcres.RetryBudgetConfig{MaxTokens: 10, TokenRatio: 0.1})
	//: successes on a full bucket are lost: it stays at ten.
	for range 50 {
		b.RecordSuccess()
	}
	for range 4 {
		b.RecordFailure()
	}
	if !b.AllowRetry() {
		t.Fatal("6 tokens of 10: retries refused, want allowed")
	}
	b.RecordFailure()
	if b.AllowRetry() {
		t.Fatal("5 tokens of 10: retries allowed, want refused — the threshold is strictly more than half")
	}
	//: one success is a tenth of a token: 5.1 is more than half.
	b.RecordSuccess()
	if !b.AllowRetry() {
		t.Fatal("5.1 tokens of 10: retries refused, want allowed")
	}
	//: down to 4.1, then nine successes bring it back to exactly half.
	b.RecordFailure()
	for range 9 {
		b.RecordSuccess()
	}
	if b.AllowRetry() {
		t.Fatal("5.0 tokens of 10: retries allowed, want refused")
	}
	b.RecordSuccess()
	if !b.AllowRetry() {
		t.Fatal("5.1 tokens of 10: retries refused, want allowed")
	}
	//: the bucket never goes below empty: twenty failures, then six tokens'
	//: worth of successes, is six tokens — not six minus the overdraft.
	for range 20 {
		b.RecordFailure()
	}
	for range 60 {
		b.RecordSuccess()
	}
	if !b.AllowRetry() {
		t.Fatal("6 tokens after an emptied bucket: retries refused — the count went negative")
	}
}

// TestRetryBudget_HugeValuesDoNotOverflow pins the clamps: the largest
// MaxTokens and an enormous TokenRatio build a working budget on every
// architecture — no count in thousandths wraps negative and refuses forever.
func TestRetryBudget_HugeValuesDoNotOverflow(t *testing.T) {
	t.Parallel()
	b := svcres.NewRetryBudget(svcres.RetryBudgetConfig{MaxTokens: math.MaxInt, TokenRatio: 1e300})
	if !b.AllowRetry() {
		t.Fatal("a full bucket of the largest size refused a retry")
	}
	b.RecordFailure()
	b.RecordSuccess()
	b.RecordSuccess()
	if !b.AllowRetry() {
		t.Fatal("after a failure and two successes: refused — a count overflowed")
	}
}

// TestRetryBudget_ConcurrentRecords pins the lock-free counter under
// contention: 1000 failures recorded from 100 goroutines on a 2000-token
// bucket leave exactly 1000 — half, refused — and one success more than that
// at a ratio of one tips it back. A lost compare-and-swap would leave it
// above half.
func TestRetryBudget_ConcurrentRecords(t *testing.T) {
	t.Parallel()
	b := svcres.NewRetryBudget(svcres.RetryBudgetConfig{MaxTokens: 2000, TokenRatio: 1})
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			for range 10 {
				b.RecordFailure()
				_ = b.AllowRetry()
			}
		})
	}
	wg.Wait()
	if b.AllowRetry() {
		t.Fatal("1000 tokens of 2000 after 1000 concurrent failures: allowed, want refused")
	}
	b.RecordSuccess()
	if !b.AllowRetry() {
		t.Fatal("1001 tokens of 2000: refused, want allowed")
	}
}

// TestRetry_BudgetStopsTheRetries pins the retry's use of the budget: a
// four-token bucket allows the retry after one failure (3 > 2) and refuses it
// after two (2 is not more than 2), ending the call with
// RETRY_BUDGET_EXCEEDED before MaxAttempts; the next call still runs its first
// attempt, and is refused a retry at once.
func TestRetry_BudgetStopsTheRetries(t *testing.T) {
	t.Parallel()
	budget := svcres.NewRetryBudget(svcres.RetryBudgetConfig{MaxTokens: 4, TokenRatio: 0.1})
	r := svcres.NewRetry(svcres.RetryConfig{
		MaxAttempts: 10, Budget: budget, Clock: clock.NewManualClock(time.Unix(1_700_000_000, 0)),
	})
	for call, want := range []int32{2, 1} {
		var attempts atomic.Int32
		err := r.Run(t.Context(), func(context.Context) error {
			attempts.Add(1)
			return errDown
		})
		if !kerrs.HasCode(err, coreres.CodeRetryBudgetExceeded) {
			t.Fatalf("call %d: Run = %v, want RETRY_BUDGET_EXCEEDED", call, err)
		}
		if !hasField(err, "cause", errDown.Error()) {
			t.Errorf("call %d: %v does not carry the last attempt's error", call, err)
		}
		if got := attempts.Load(); got != want {
			t.Errorf("call %d: %d attempts, want %d", call, got, want)
		}
	}
}

// TestRetry_WhatTheBudgetHears pins what the retry records: a success, each
// retryable failure — the last attempt's included —, and nothing for a
// deterministic error or a caller who left. A nil budget is the policy as it
// was, which every other suite in this package runs.
func TestRetry_WhatTheBudgetHears(t *testing.T) {
	t.Parallel()
	errBad := errors.New("bad request")
	type tc struct {
		name          string
		ctx           func(t *testing.T) context.Context
		results       []error
		wantSuccesses int32
		wantFailures  int32
		wantQuestions int32
	}
	live := func(t *testing.T) context.Context {
		t.Helper()
		return t.Context()
	}
	tests := []tc{
		{name: "a first success", ctx: live, results: []error{nil}, wantSuccesses: 1},
		{name: "a failure then a success", ctx: live, results: []error{errDown, nil}, wantSuccesses: 1, wantFailures: 1, wantQuestions: 1},
		{name: "every attempt fails", ctx: live, results: []error{errDown, errDown, errDown}, wantFailures: 3, wantQuestions: 2},
		{name: "a deterministic error", ctx: live, results: []error{errBad}},
		{name: "a caller who left", ctx: cancelledContext, results: []error{context.Canceled}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			spy := newSpy(true)
			r := svcres.NewRetry(svcres.RetryConfig{
				MaxAttempts: len(c.results), Budget: spy,
				Clock:     clock.NewManualClock(time.Unix(1_700_000_000, 0)),
				Retryable: func(err error) bool { return !errors.Is(err, errBad) },
			})
			var attempt atomic.Int32
			_ = r.Run(c.ctx(t), func(context.Context) error {
				return c.results[attempt.Add(1)-1]
			})
			if s, f, q := spy.successes.Load(), spy.failures.Load(), spy.questions.Load(); s != c.wantSuccesses || f != c.wantFailures || q != c.wantQuestions {
				t.Fatalf("successes=%d failures=%d questions=%d, want %d %d %d",
					s, f, q, c.wantSuccesses, c.wantFailures, c.wantQuestions)
			}
		})
	}
}

// TestRetry_OnRetry pins the hook: called before each retry's wait, on the
// retry's goroutine, with the retry's number, its cause and the wait about to
// start — the second retry's doubled — and not for a retry the budget refused.
//
// GOROUTINE LIFECYCLE: one goroutine runs the retry; the test drives every
// wait by advancing the clock and reads its result before returning.
func TestRetry_OnRetry(t *testing.T) {
	t.Parallel()
	type heard struct {
		attempt int
		err     error
		delay   time.Duration
	}
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	spy := newSpy(true)
	var mu sync.Mutex
	var log []heard
	r := svcres.NewRetry(svcres.RetryConfig{
		MaxAttempts: 4, BaseDelay: time.Second, Clock: manual, Budget: spy,
		OnRetry: func(attempt int, err error, delay time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			log = append(log, heard{attempt, err, delay})
			//: the third failure's retry is refused.
			if attempt == 2 {
				spy.allow.Store(false)
			}
		},
	})
	done := make(chan error, 1)
	go func() {
		done <- r.Run(t.Context(), func(context.Context) error { return errDown })
	}()
	//: heard before the wait: the timer is armed after the hook returned.
	manual.BlockUntil(1)
	mu.Lock()
	if len(log) != 1 || log[0] != (heard{1, errDown, time.Second}) {
		t.Fatalf("before the first wait: heard %v, want [{1 down 1s}]", log)
	}
	mu.Unlock()
	manual.Advance(time.Second)
	manual.BlockUntil(1)
	manual.Advance(2 * time.Second)

	if err := <-done; !kerrs.HasCode(err, coreres.CodeRetryBudgetExceeded) {
		t.Fatalf("Run = %v, want RETRY_BUDGET_EXCEEDED", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(log) != 2 || log[1] != (heard{2, errDown, 2 * time.Second}) {
		t.Fatalf("heard %v, want two retries, the second waiting 2s, and none for the refused one", log)
	}
}

// TestHedge_BudgetRefusesTheDuplicate pins the hedge's use of the budget: once
// the delay elapses the budget is asked, and its refusal issues no duplicate —
// the call proceeds on its first attempt, as when MaxInFlight is reached. A
// caller who leaves records nothing.
//
// GOROUTINE LIFECYCLE: one goroutine runs the hedge; its first copy blocks
// until the caller's context is cancelled, which the test does, and the test
// reads the result before returning.
func TestHedge_BudgetRefusesTheDuplicate(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	spy := newSpy(false)
	spy.asked = make(chan struct{}, 1)
	h := svcres.NewHedge(svcres.HedgeConfig{
		Idempotent: true, Delay: time.Second, MaxHedges: 1, MaxInFlight: 1, Clock: manual, Budget: spy,
	})
	ctx, cancel := context.WithCancel(t.Context())
	var copies atomic.Int32
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.Run(ctx, func(ctx context.Context) error {
			if copies.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	manual.BlockUntil(1)
	manual.Advance(time.Second)
	//: the budget was asked, and said no.
	<-spy.asked
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the caller's cancellation", err)
	}
	if got := copies.Load(); got != 1 {
		t.Fatalf("%d copies, want the first alone — the budget refused the duplicate", got)
	}
	if s, f := spy.successes.Load(), spy.failures.Load(); s != 0 || f != 0 {
		t.Fatalf("successes=%d failures=%d after the caller left, want nothing recorded", s, f)
	}
}

// TestHedge_WhatTheBudgetHears pins the hedge's records: one per decided call,
// a success or a failure.
func TestHedge_WhatTheBudgetHears(t *testing.T) {
	t.Parallel()
	for _, result := range []error{nil, errDown} {
		spy := newSpy(true)
		h := svcres.NewHedge(svcres.HedgeConfig{
			Idempotent: true, Delay: time.Hour, MaxInFlight: 1, Budget: spy,
			Clock: clock.NewManualClock(time.Unix(1_700_000_000, 0)),
		})
		_ = h.Run(t.Context(), func(context.Context) error { return result })
		wantS, wantF := int32(1), int32(0)
		if result != nil {
			wantS, wantF = 0, 1
		}
		if s, f := spy.successes.Load(), spy.failures.Load(); s != wantS || f != wantF {
			t.Fatalf("result %v: successes=%d failures=%d, want %d %d", result, s, f, wantS, wantF)
		}
	}
}

