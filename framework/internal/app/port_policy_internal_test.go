package app

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
)

// A failure is the dependency's when the start answers it as a 5xx, a 408
// or a 429; a refusal of the caller's own is not, nor a caller that left,
// nor a refusal a policy made itself.
func TestATransientFailureIsTheDependencys(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", Unavailable("down"), true},
		{"internal", errors.New("broken"), true},
		{"timeout", resilience.TimeoutExceeded, true},
		{"rate limited", resilience.RateLimited, true},
		{"too slow", NewError(408, "timeout", "too slow"), true},
		{"not found", NotFound("no such item"), false},
		{"invalid", Invalid("no"), false},
		{"canceled", context.Canceled, false},
		{"open", resilience.CircuitOpen, false},
		{"throttled", resilience.Throttled, false},
		{"exhausted", resilience.RetryExhausted, false},
		{"budget", resilience.RetryBudgetExceeded, false},
		{"misconfigured", resilience.PolicyMisconfigured, false},
		{"none", nil, false},
	} {
		if got := transient(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A throttle refuses on the caller's side, without calling, once the
// dependency refused more than its share of the recent calls; a refusal of
// the caller's own counts as an accept.
func TestAThrottleRefusesOnTheCallersSide(t *testing.T) {
	g := &portGuards{}
	Throttle(2).portPolicyConfigure(g)
	g.settle(nil, nil)
	g.layers[0].(*policyLayer).p.guard.(*throttleGuard).random = func() float64 { return 0 }
	calls := 0
	failing := func(context.Context) error {
		calls++
		return Unavailable("down")
	}
	if err := g.run(t.Context(), &portCall{}, failing); errors.Is(err, resilience.Throttled) {
		t.Fatalf("the first call throttled")
	}
	if err := g.run(t.Context(), &portCall{}, failing); !errors.Is(err, resilience.Throttled) || calls != 1 {
		t.Errorf("after a refusal: %v, %d calls", err, calls)
	}

	h := &portGuards{}
	Throttle(2).portPolicyConfigure(h)
	h.settle(nil, nil)
	h.layers[0].(*policyLayer).p.guard.(*throttleGuard).random = func() float64 { return 0 }
	refused := func(context.Context) error {
		calls++
		return NotFound("no such item")
	}
	for range 3 {
		if err := h.run(t.Context(), &portCall{}, refused); errors.Is(err, resilience.Throttled) {
			t.Fatalf("a caller's refusal throttled")
		}
	}
}

// Whatever the order a port's policies are given in, they compose the
// outermost first as retry, hedge, breaker, throttle, timeout.
func TestAPortsPoliciesComposeInOneOrder(t *testing.T) {
	g := &portGuards{}
	for _, o := range []PortPolicyConfigurer{Timeout(1), Throttle(2), Breaker(1, 1), Idempotent(), Hedge(1, 1), Retry(1, 1)} {
		o.portPolicyConfigure(g)
	}
	g.settle(nil, nil)
	var ranks []int
	for _, l := range g.layers {
		ranks = append(ranks, l.rank())
	}
	want := []int{rankRetry, rankHedge, rankBreaker, rankThrottle, rankTimeout}
	if len(ranks) != len(want) {
		t.Fatalf("%v, want %v", ranks, want)
	}
	for i := range want {
		if ranks[i] != want[i] {
			t.Errorf("%v, want %v", ranks, want)
		}
	}
	if g.budget == nil || g.budget.tokens != defaultBudgetTokens || g.budget.ratio != defaultBudgetRatio {
		t.Errorf("the retry and the hedge share no default budget: %+v", g.budget)
	}
}
