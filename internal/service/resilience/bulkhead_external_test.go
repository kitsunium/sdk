// Package resilience_test — the bulkhead as a caller configures it.
package resilience_test

import (
	"context"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewBulkhead pins the clamp. A limit below one would admit nothing, which
// is a policy that rejects every call while reporting the error it uses when it
// is working — so a non-positive limit becomes one slot instead.
func TestNewBulkhead(t *testing.T) {
	t.Parallel()
	type tc struct {
		name          string
		maxConcurrent int
	}
	tests := []tc{
		{"one slot", 1},
		{"several slots", 8},
		{"zero clamps to one", 0},
		{"a negative limit clamps to one", -5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := svcres.NewBulkhead(c.maxConcurrent)
		if b == nil {
			t.Fatal("NewBulkhead returned no runner")
		}
		//: whatever the limit, a lone caller must always be admitted.
		ran := false
		err := b.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if !ran {
			t.Error("the lone caller was not admitted")
		}
		//: and a second sequential call is admitted too, proving the slot came
		//: back rather than being consumed once.
		if err := b.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
			if kerrs.HasCode(err, coreres.CodeBulkheadFull) {
				t.Error("the slot was not released after the first call")
			}
			t.Fatalf("the second Run = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
