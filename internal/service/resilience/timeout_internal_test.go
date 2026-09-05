package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// Test_Timeout_LateSuccessStillTimesOut pins the deadline's authority: an
// Operation that ignores ctx and reports success AFTER the deadline expired
// must still surface TimeoutExceeded.
//
// The old guard only consulted dctx.Err() when op returned a non-nil error, so
// a ctx-ignoring op returning nil made Run report success and silently void the
// policy. The doc promises degraded precision ("op granularity") for such an
// op — not an abandoned deadline.
func Test_Timeout_LateSuccessStillTimesOut(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		op      coreres.Operation
		wantErr bool
	}
	tests := []tc{
		{
			//: the defect case — ignores ctx, overruns, then reports success.
			"ignores ctx and succeeds late",
			func(context.Context) error { time.Sleep(40 * time.Millisecond); return nil },
			true,
		},
		{
			//: a well-behaved op that finishes in time still succeeds.
			"finishes in time",
			func(context.Context) error { return nil },
			false,
		},
		{
			//: an op honouring ctx surfaces the deadline itself.
			"honours ctx",
			func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			true,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := NewTimeout(10*time.Millisecond).Run(t.Context(), tc.op)
		//: contract: an expired deadline always reports TimeoutExceeded.
		if tc.wantErr {
			if !errors.Is(err, coreres.TimeoutExceeded) {
				t.Errorf("%s: err=%v, want TimeoutExceeded", tc.name, err)
			}
			return
		}
		//: an in-time operation propagates its own (nil) outcome.
		if err != nil {
			t.Errorf("%s: err=%v, want nil", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_Timeout_ParentCancelNotRelabelled guards the boundary: a parent
// cancellation is NOT a deadline, so it must propagate as-is rather than being
// relabelled TimeoutExceeded by the broadened check.
func Test_Timeout_ParentCancelNotRelabelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := NewTimeout(time.Hour).Run(ctx, func(c context.Context) error { return c.Err() })
	//: the parent cancel surfaces context.Canceled, untouched by the policy.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want context.Canceled", err)
	}
	//: and it must NOT be mislabelled as a timeout.
	if errors.Is(err, coreres.TimeoutExceeded) {
		t.Error("parent cancellation was relabelled TimeoutExceeded")
	}
}
