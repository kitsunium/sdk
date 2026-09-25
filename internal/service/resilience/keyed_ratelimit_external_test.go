// Package resilience_test — the keyed rate limiter as a caller configures it.
package resilience_test

import (
	"context"
	"math"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// keyCtx is the context key the tests charge calls to.
type keyCtx struct{}

// keyOf is the Key function the tests configure: the string stored in the
// context, as a framework would store the authenticated user or the client's
// address.
func keyOf(ctx context.Context) string {
	key, _ := ctx.Value(keyCtx{}).(string)
	return key
}

// as returns a context whose calls are charged to key.
func as(parent context.Context, key string) context.Context {
	return context.WithValue(parent, keyCtx{}, key)
}

// admit runs a no-op through runner as key and reports whether it was
// admitted, failing the test on any outcome other than admission or
// RATE_LIMITED.
func admit(t *testing.T, runner coreres.Runner, key string) bool {
	t.Helper()
	err := runner.Run(as(t.Context(), key), func(context.Context) error { return nil })
	if err == nil {
		return true
	}
	if !kerrs.HasCode(err, coreres.CodeRateLimited) {
		t.Fatalf("Run as %q = %v, want nil or RATE_LIMITED", key, err)
	}
	return false
}

// TestNewKeyedRateLimiter pins the four refusals. Each field it refuses has a
// zero with two readings, and one reading of each disarms the limiter while
// every call keeps succeeding: a zero IdleTimeout forgets every key at once
// and hands every call a full bucket, a zero MaxKeys is either no bound on
// memory or no admission at all.
func TestNewKeyedRateLimiter(t *testing.T) {
	t.Parallel()
	valid := svcres.KeyedRateLimiterConfig{
		Rate: 1, Burst: 1, Key: keyOf, MaxKeys: 10, IdleTimeout: time.Minute,
	}
	type tc struct {
		name   string
		mutate func(*svcres.KeyedRateLimiterConfig)
		//: the knob a refusal must name, or "" when the policy is valid.
		wantKnob string
	}
	tests := []tc{
		{name: "a valid configuration", mutate: func(*svcres.KeyedRateLimiterConfig) {}},
		{name: "a zero burst clamps to one", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.Burst = 0 }},
		{name: "a zero rate", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.Rate = 0 }, wantKnob: "Rate"},
		{name: "a NaN rate", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.Rate = math.NaN() }, wantKnob: "Rate"},
		{name: "an infinite rate", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.Rate = math.Inf(1) }, wantKnob: "Rate"},
		{name: "no key", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.Key = nil }, wantKnob: "Key"},
		{name: "a zero key bound", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.MaxKeys = 0 }, wantKnob: "MaxKeys"},
		{name: "a negative key bound", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.MaxKeys = -1 }, wantKnob: "MaxKeys"},
		{name: "a zero idle timeout", mutate: func(c *svcres.KeyedRateLimiterConfig) { c.IdleTimeout = 0 }, wantKnob: "IdleTimeout"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := valid
		c.mutate(&cfg)
		ran := false
		err := svcres.NewKeyedRateLimiter(cfg).Run(as(t.Context(), "ann"), func(context.Context) error {
			ran = true
			return nil
		})
		if c.wantKnob == "" {
			if err != nil || !ran {
				t.Fatalf("Run = %v (ran %v), want the first call admitted", err, ran)
			}
			return
		}
		if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
			t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
		}
		if ran {
			t.Error("a misconfigured limiter ran the operation")
		}
		if got := knobOf(err); got != c.wantKnob {
			t.Errorf("the refusal names %q, want %q", got, c.wantKnob)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// knobOf returns the knob a PolicyMisconfigured refusal names.
func knobOf(err error) string {
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == "knob" {
			return field.StringValue()
		}
	}
	return ""
}

// TestAKeyThatEmptiesItsBucketDoesNotEmptyAnyoneElses is the property the
// limiter exists for: on a sign-in endpoint behind ONE bucket, a single client
// guessing passwords locks every other user out.
func TestAKeyThatEmptiesItsBucketDoesNotEmptyAnyoneElses(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	limiter := svcres.NewKeyedRateLimiter(svcres.KeyedRateLimiterConfig{
		Rate: 1, Burst: 2, Key: keyOf, MaxKeys: 100, IdleTimeout: time.Hour, Clock: manual,
	})
	//: the attacker spends its burst and is refused.
	for call := range 2 {
		if !admit(t, limiter, "mallory") {
			t.Fatalf("call %d of a burst of two was refused", call+1)
		}
	}
	if admit(t, limiter, "mallory") {
		t.Fatal("a third call inside the same instant was admitted")
	}
	//: everybody else still has a full bucket.
	for call := range 2 {
		if !admit(t, limiter, "alice") {
			t.Errorf("call %d of another key paid for the attacker's calls", call+1)
		}
	}
	//: and the attacker's bucket refills at its rate, on the injected clock.
	manual.Advance(time.Second)
	if !admit(t, limiter, "mallory") {
		t.Error("one second at one token per second did not refill a token")
	}
}

// TestAnIdleKeyIsForgottenAndABusyOneIsNot pins the sliding idle window: a key
// charged within IdleTimeout keeps its bucket however long it has existed, and
// one left alone for IdleTimeout starts over full — deterministically, on the
// key's own next call, rather than whenever some other new key happened to
// trigger a sweep.
func TestAnIdleKeyIsForgottenAndABusyOneIsNot(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	//: a rate slow enough that refilling cannot explain an admission.
	limiter := svcres.NewKeyedRateLimiter(svcres.KeyedRateLimiterConfig{
		Rate: 0.0001, Burst: 1, Key: keyOf, MaxKeys: 100, IdleTimeout: 10 * time.Minute, Clock: manual,
	})
	for _, key := range []string{"busy", "idle"} {
		if !admit(t, limiter, key) {
			t.Fatalf("the fresh key %q was refused its first call", key)
		}
	}
	//: the busy key keeps calling every nine minutes, and is refused each
	//: time: its window slides, so its empty bucket is never forgotten.
	for range 3 {
		manual.Advance(9 * time.Minute)
		if admit(t, limiter, "busy") {
			t.Fatal("a key charged within its idle window was handed a fresh bucket")
		}
	}
	//: twenty-seven minutes on, the idle key has been left alone long enough.
	if !admit(t, limiter, "idle") {
		t.Error("a key idle past its timeout was not forgotten")
	}
}
