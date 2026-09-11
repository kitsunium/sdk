// Package resilience_test — the hedging policy as a caller configures it.
package resilience_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// hedgeDelay is long enough that an operation returning immediately can never
// race it, so "no duplicate was issued" is an assertion and not a coin flip.
const hedgeDelay time.Duration = time.Hour

// TestNewHedge pins the three refusals and the one clamp, which together are
// ADR 0031 applied to the most dangerous policy in the package.
//
// The zero HedgeConfig is refused three times over, and each refusal answers a
// different way the zero value would betray the caller:
//
//   - Idempotent false — the SDK cannot detect whether duplicating the
//     operation double-charges a card. Defaulting it to true would let a caller
//     reach concurrent duplication by copying a config and deleting a field,
//     and the symptom would be a silent double effect, never an error.
//   - Delay zero — a zero threshold duplicates EVERY call the instant it
//     starts. That is a load amplifier wearing a resilience policy's name,
//     doing the exact opposite of its job to the dependency it guards. The
//     useful value is the caller's measured p95, which the SDK cannot guess
//     (the RateLimiterConfig.Rate argument, in latency).
//   - MaxInFlight zero — the load cap. This is the rarer knob where BOTH
//     directions of a guess are harmful: a conservative SDK default silently
//     stops hedging under exactly the load hedging was bought for (inert, ADR
//     0031's own failure mode), and a generous one hands back the amplifier.
//
// MaxHedges is the contrast that locates the line: "issue at least one
// duplicate" is an obvious floor, so it clamps, exactly as Burst and the
// bulkhead limit do.
func TestNewHedge(t *testing.T) {
	t.Parallel()
	//: the knobs a valid config needs; each case below breaks exactly one.
	valid := svcres.HedgeConfig{
		Idempotent:  true,
		Delay:       hedgeDelay,
		MaxHedges:   2,
		MaxInFlight: 4,
	}

	type tc struct {
		name string
		cfg  svcres.HedgeConfig
		//: whether every call must be refused as a misconfiguration.
		wantMisconfigured bool
	}
	tests := []tc{
		{name: "a fully specified config", cfg: valid},
		{
			//: a zero budget would make the policy inert, so it clamps to one
			//: duplicate — the minimum that still hedges.
			name: "a zero MaxHedges clamps to one duplicate",
			cfg:  svcres.HedgeConfig{Idempotent: true, Delay: hedgeDelay, MaxInFlight: 1},
		},
		{
			name: "a negative MaxHedges clamps to one duplicate",
			cfg:  svcres.HedgeConfig{Idempotent: true, Delay: hedgeDelay, MaxHedges: -3, MaxInFlight: 1},
		},
		{
			name:              "the zero config is refused",
			cfg:               svcres.HedgeConfig{},
			wantMisconfigured: true,
		},
		{
			name:              "an unasserted idempotence is refused",
			cfg:               svcres.HedgeConfig{Delay: hedgeDelay, MaxHedges: 2, MaxInFlight: 4},
			wantMisconfigured: true,
		},
		{
			name:              "a zero Delay is refused",
			cfg:               svcres.HedgeConfig{Idempotent: true, MaxHedges: 2, MaxInFlight: 4},
			wantMisconfigured: true,
		},
		{
			name:              "a negative Delay is refused",
			cfg:               svcres.HedgeConfig{Idempotent: true, Delay: -time.Second, MaxHedges: 2, MaxInFlight: 4},
			wantMisconfigured: true,
		},
		{
			name:              "a zero MaxInFlight is refused",
			cfg:               svcres.HedgeConfig{Idempotent: true, Delay: hedgeDelay, MaxHedges: 2},
			wantMisconfigured: true,
		},
		{
			name:              "a negative MaxInFlight is refused",
			cfg:               svcres.HedgeConfig{Idempotent: true, Delay: hedgeDelay, MaxHedges: 2, MaxInFlight: -1},
			wantMisconfigured: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewHedge(c.cfg)

		var runs atomic.Int64
		err := r.Run(t.Context(), func(context.Context) error {
			runs.Add(1)
			return nil
		})

		if c.wantMisconfigured {
			//: the refusal names the policy as misconfigured rather than
			//: reporting the operation's own outcome.
			if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
			}
			//: and — the point, for this policy — it ran nothing at all rather
			//: than running the operation twice.
			if got := runs.Load(); got != 0 {
				t.Errorf("a misconfigured hedge ran the operation %d times", got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		//: a working hedge on a fast operation issues NO duplicate: the
		//: duplication is gated on the delay, never on the call itself.
		if got := runs.Load(); got != 1 {
			t.Errorf("the operation ran %d times, want exactly 1 — a fast call must not be duplicated", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
