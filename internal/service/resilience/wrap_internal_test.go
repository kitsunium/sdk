// Package resilience — the shared sentinel-wrapping helper.
package resilience

import (
	"context"
	"errors"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapAs pins the origin-wins decision every policy depends on.
//
// The SENTINEL is the origin, so the policy's own code survives whatever the
// operation returned. That matters because the cause here is caller code: an
// operation is free to return an *errs.Error carrying a code from a completely
// different domain, and letting it become the origin would tell the caller their
// request was, say, UNKNOWN_SCHEME rather than RATE_LIMITED.
func Test_wrapAs(t *testing.T) {
	t.Parallel()
	//: a typed cause is exactly the case origin-wins exists for.
	foreign := kerrs.Define(kerrs.Code(0x00_02_1D_01), "FOREIGN_REASON",
		"a foreign public message", "a foreign private message")

	type tc struct {
		name         string
		sentinel     *kerrs.Error
		cause        error
		wantHasCause bool
	}
	tests := []tc{
		{"an exhausted retry with no cause", coreres.RetryExhausted, nil, false},
		{"an exhausted retry with a plain cause", coreres.RetryExhausted, errors.New("dial refused"), true},
		{"an exhausted retry with a typed cause", coreres.RetryExhausted, foreign, true},
		{"a rate-limit rejection", coreres.RateLimited, nil, false},
		{"a full bulkhead", coreres.BulkheadFull, nil, false},
		{"an open circuit", coreres.CircuitOpen, nil, false},
		{"an exceeded timeout", coreres.TimeoutExceeded, context.DeadlineExceeded, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := wrapAs(c.sentinel, c.cause)
		if got == nil {
			t.Fatal("wrapAs returned nil")
		}

		//: the policy's code survives even a typed cause.
		wantCode, _ := kerrs.CodeOf(c.sentinel)
		if !kerrs.HasCode(got, wantCode) {
			t.Errorf("wrapAs = %v, want code %v", got, wantCode)
		}
		//: and the caller can match the sentinel itself, which is how a policy
		//: decision is told apart from an operation failure.
		if !errors.Is(got, c.sentinel) {
			t.Errorf("errors.Is(err, sentinel) = false for %v", got)
		}

		hasCause := false
		for _, f := range kerrs.FieldsOf(got) {
			if f.Key() == "cause" {
				hasCause = true
			}
		}
		if hasCause != c.wantHasCause {
			t.Errorf("a cause field is present = %v, want %v", hasCause, c.wantHasCause)
		}
		//: a nil cause returns the bare sentinel, which lets a caller compare
		//: it by identity without allocating.
		if c.cause == nil && got != error(c.sentinel) {
			t.Error("wrapAs with a nil cause did not return the bare sentinel")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
