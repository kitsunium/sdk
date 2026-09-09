// Package resilience — the fallback runner's outcome matrix.
package resilience

import (
	"context"
	"errors"
	"strings"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errPrimary and errSecondary are distinguishable on sight, so a test that
// reports the wrong half of a double failure says which half it got.
var (
	errPrimary   = errors.New("the primary failed")
	errSecondary = errors.New("the fallback failed too")
)

// Test_fallbackRunner_Run pins the four outcomes of the policy, and above all
// the double-failure one.
//
// When both halves fail, reporting only one of them makes the outcome
// undiagnosable: "the fallback failed" never says what it was covering for, and
// "the primary failed" never says that plan B was tried and also broke. So the
// policy reports its OWN outcome — FALLBACK_FAILED — and carries both messages
// as fields. Promoting either half to the wrap origin was the other candidate
// and is worse: origin-wins would let an *errs.Error half hijack the policy's
// code, which is the rule wrapAs exists to enforce.
func Test_fallbackRunner_Run(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what each half returns.
		primaryErr   error
		secondaryErr error
		//: the classifier gating whether the fallback runs at all.
		retryable func(error) bool
		//: expectations.
		wantFallbackRan bool
		wantErr         error
		wantBothFailed  bool
	}
	tests := []tc{
		{
			//: nothing failed — plan B is never consulted.
			name: "a successful primary skips the fallback",
		},
		{
			//: the policy's purpose: the primary error is absorbed.
			name:            "a failing primary is masked by a working fallback",
			primaryErr:      errPrimary,
			wantFallbackRan: true,
		},
		{
			//: both halves down — the composed outcome, with both messages.
			name:            "both halves failing reports both",
			primaryErr:      errPrimary,
			secondaryErr:    errSecondary,
			wantFallbackRan: true,
			wantBothFailed:  true,
		},
		{
			//: a deterministic primary failure is the caller's own; serving a
			//: substitute answer for it would hide a bug behind a stale success.
			name:       "a deterministic primary failure is returned verbatim",
			primaryErr: errPrimary,
			retryable:  func(error) bool { return false },
			wantErr:    errPrimary,
		},
		{
			//: an accepting classifier is the nil default, said out loud.
			name:            "an accepting classifier falls back",
			primaryErr:      errPrimary,
			retryable:       func(error) bool { return true },
			wantFallbackRan: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fallbackRan := false
		r := NewFallback(FallbackConfig{
			Fallback: func(context.Context) error {
				fallbackRan = true
				return c.secondaryErr
			},
			Retryable: c.retryable,
		})

		err := r.Run(t.Context(), func(context.Context) error { return c.primaryErr })

		if fallbackRan != c.wantFallbackRan {
			t.Errorf("the fallback ran = %v, want %v", fallbackRan, c.wantFallbackRan)
		}
		if c.wantBothFailed {
			assertBothFailed(t, err)
			return
		}
		if c.wantErr != nil {
			//: verbatim: not relabelled, not wrapped in the policy sentinel.
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Run = %v, want %v", err, c.wantErr)
			}
			if errs.HasCode(err, coreres.CodeFallbackFailed) {
				t.Error("a verbatim error was relabelled FALLBACK_FAILED")
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertBothFailed checks the double-failure contract: the policy's own code,
// and BOTH halves' messages preserved as fields.
func assertBothFailed(t *testing.T, err error) {
	t.Helper()
	//: the composed outcome is matchable by code, not by string comparison.
	if !errs.HasCode(err, coreres.CodeFallbackFailed) {
		t.Fatalf("Run = %v, want FALLBACK_FAILED", err)
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("Run = %v, want an *errs.Error", err)
	}
	//: neither half may be dropped — that is the whole decision this test pins.
	fields := map[string]string{}
	for _, f := range typed.Fields() {
		fields[f.Key()] = f.StringValue()
	}
	for key, want := range map[string]string{
		"primary":  errPrimary.Error(),
		"fallback": errSecondary.Error(),
	} {
		got, ok := fields[key]
		if !ok {
			t.Errorf("field %q missing — that half of the failure is undiagnosable", key)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("field %q = %q, want it to carry %q", key, got, want)
		}
	}
}

// Test_fallbackRunner_RunCancelled pins that a cancelled context ends the call
// as a cancellation rather than as a fallback fault.
//
// Running plan B under a context that is already dead would fail it the same
// way the primary failed, and the policy would then report FALLBACK_FAILED for
// what is simply the caller going away — the retry policy stops on the same
// condition, for the same reason.
func Test_fallbackRunner_RunCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())

	fallbackRan := false
	r := NewFallback(FallbackConfig{
		Fallback: func(context.Context) error {
			fallbackRan = true
			return nil
		},
	})

	err := r.Run(ctx, func(context.Context) error {
		//: the caller goes away while the primary is running.
		cancel()
		return errPrimary
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if fallbackRan {
		t.Error("the fallback ran under a dead context")
	}
	if errs.HasCode(err, coreres.CodeFallbackFailed) {
		t.Error("a cancellation was reported as FALLBACK_FAILED")
	}
}
