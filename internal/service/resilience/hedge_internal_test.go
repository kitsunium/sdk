// Package resilience — the hedging runner's race, its budget and its cap.
package resilience

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// tick is the hedging delay used by the racing tests: short enough to keep
	// them quick, long enough that an instant operation never races it.
	tick time.Duration = 20 * time.Millisecond
	// settle is a window of several ticks used to prove a duplicate was NOT
	// issued. Asserting an absence needs a deadline; this is it.
	settle time.Duration = 300 * time.Millisecond
)

// errSlow and errFast identify which copy of an operation produced a failure.
var (
	errSlow = errors.New("the slow attempt failed")
	errFast = errors.New("the fast attempt failed")
)

// Test_hedge_Run pins the race itself: the first attempt to FINISH wins,
// whichever copy it came from — with the one asymmetry that a failure does not
// end the race while another attempt is still outstanding.
//
// That asymmetry is the policy: a duplicate exists precisely so a slow attempt
// is not the last word, and abandoning the race on the first failure would give
// the duplicate nothing to win.
func Test_hedge_Run(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the first copy does, and what every later copy does.
		firstBlocks bool
		firstDelay  time.Duration
		firstErr    error
		laterErr    error
		//: expectations.
		wantErr   error
		wantCalls int64
	}
	tests := []tc{
		{
			//: the headline case: the original hangs, the duplicate answers.
			name:        "a duplicate rescues a hung first attempt",
			firstBlocks: true,
			wantCalls:   2,
		},
		{
			//: hedging acts on latency, never on failure. A first attempt that
			//: fails FAST ends the call there: replaying a failure is retrying,
			//: and NewRetry already does that. It is also what keeps the added
			//: load proportional to slowness rather than to breakage.
			name:      "an immediate failure is not hedged",
			firstErr:  errSlow,
			wantErr:   errSlow,
			wantCalls: 1,
		},
		{
			//: an instant success needs no help and gets no duplicate.
			name:      "an immediate success is not hedged",
			wantCalls: 1,
		},
		{
			//: every copy failed. The policy returns the operation's own error
			//: — the first one to arrive, by the same first-to-finish rule that
			//: decides a success — rather than inventing a sentinel that would
			//: hide what actually went wrong.
			name:       "every attempt failing returns the first failure to arrive",
			firstDelay: settle,
			firstErr:   errSlow,
			laterErr:   errFast,
			wantErr:    errFast,
			wantCalls:  2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var calls atomic.Int64
		r := NewHedge(HedgeConfig{Idempotent: true, Delay: tick, MaxHedges: 1, MaxInFlight: 1})

		err := r.Run(t.Context(), func(ctx context.Context) error {
			//: the first copy is the one that misbehaves; later copies are the
			//: hedge, and they answer promptly.
			if calls.Add(1) != 1 {
				return c.laterErr
			}
			if c.firstBlocks {
				//: hang until the winner's cancellation arrives.
				<-ctx.Done()
				return ctx.Err()
			}
			//: a zero delay returns immediately.
			time.Sleep(c.firstDelay)
			return c.firstErr
		})

		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Run = %v, want %v", err, c.wantErr)
			}
			//: the operation's own error, never relabelled by the policy.
			if errs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Error("a working hedge reported a misconfiguration")
			}
		} else if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if got := calls.Load(); got != c.wantCalls {
			t.Errorf("the operation ran %d times, want %d", got, c.wantCalls)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hedge_RunCapsDuplicatesInFlight is the load-amplification guard, and the
// reason MaxInFlight exists at all.
//
// A dependency that has gone slow makes EVERY in-flight call want a duplicate
// at the same moment, so an uncapped hedge adds load exactly when the
// dependency has least to spare and deepens the outage it was meant to hide.
// With a cap of one, a call whose budget allows five duplicates must still
// issue exactly one — and then keep going on what it has, rather than failing:
// reaching the cap degrades the policy to no-hedging, never to a rejection.
//
// Goroutine lifecycle: exactly one, running the hedged call. It cannot outlive
// the test — every copy it starts parks on release (closed below) or on
// t.Context(), which the framework cancels when the test ends.
func Test_hedge_RunCapsDuplicatesInFlight(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var calls atomic.Int64
	r := NewHedge(HedgeConfig{Idempotent: true, Delay: tick, MaxHedges: 5, MaxInFlight: 1})

	done := make(chan error, 1)
	//: the call runs off to one side so the assertions can watch it hedge.
	go func() {
		done <- r.Run(t.Context(), func(ctx context.Context) error {
			calls.Add(1)
			//: every copy parks, so each duplicate keeps holding its slot.
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	//: wait for the cap to be reached rather than assuming a tick count — the
	//: assertion is about the ceiling, not about scheduling speed.
	const wantInFlight int64 = 2
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < wantInFlight && time.Now().Before(deadline) {
		time.Sleep(tick / 2)
	}
	if got := calls.Load(); got != wantInFlight {
		t.Fatalf("the operation ran %d times, want %d (one first attempt + one capped duplicate)", got, wantInFlight)
	}
	//: several more ticks must not add a third copy: the cap holds for as long
	//: as the duplicate is actually running, not merely until Run notices.
	time.Sleep(settle)
	if got := calls.Load(); got != wantInFlight {
		t.Errorf("the operation ran %d times after %s, want the cap to hold at %d", got, settle, wantInFlight)
	}

	//: releasing the copies ends the call normally — the cap never rejected it.
	//: (An earlier t.Fatal needs no cleanup here: t.Context() is cancelled when
	//: the test ends, which is the other way every copy unblocks.)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil — reaching the cap must degrade to no-hedging, not reject", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hedged call never returned")
	}
}

// Test_hedge_RunCancelled pins that the caller's cancellation ends the race and
// is reported as itself, not as an operation failure.
//
// Goroutine lifecycle: exactly one, which sleeps once and cancels the call. It
// cannot outlive the test because it never blocks on anything.
func Test_hedge_RunCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	r := NewHedge(HedgeConfig{Idempotent: true, Delay: tick, MaxHedges: 2, MaxInFlight: 2})

	//: cancel the call once the race is under way.
	go func() {
		time.Sleep(tick)
		cancel()
	}()

	err := r.Run(ctx, func(ctx context.Context) error {
		//: every copy waits for the cancellation.
		<-ctx.Done()
		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

// Test_hedge_claimHedge pins the two independent gates on issuing a duplicate:
// the CALL's own budget, and the RUNNER-wide in-flight cap. They are separate
// because they answer different questions — how aggressively one call may
// hedge, versus how much duplicate load the dependency may be asked to carry
// while every caller hedges at once.
func Test_hedge_claimHedge(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the runner's shape.
		maxHedges   int
		maxInFlight int
		//: slots already taken before the claim.
		held int
		//: attempts this call has already launched (the first one included).
		launched int
		want     bool
	}
	tests := []tc{
		{name: "budget and cap both free", maxHedges: 1, maxInFlight: 1, launched: 1, want: true},
		{name: "a wider budget still has room", maxHedges: 3, maxInFlight: 3, launched: 3, want: true},
		{name: "the call's budget is spent", maxHedges: 1, maxInFlight: 4, launched: 2},
		{name: "the call's budget is spent even with a wide cap", maxHedges: 2, maxInFlight: 9, launched: 3},
		{name: "the in-flight cap is full", maxHedges: 5, maxInFlight: 1, held: 1, launched: 1},
		{name: "the cap is full with budget to spare", maxHedges: 5, maxInFlight: 2, held: 2, launched: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := &hedge{delay: tick, maxHedges: c.maxHedges, slots: make(chan struct{}, c.maxInFlight)}
		for range c.held {
			if !h.acquire() {
				t.Fatalf("could not pre-hold %d slots", c.held)
			}
		}

		got := h.claimHedge(c.launched)

		if got != c.want {
			t.Fatalf("claimHedge(%d) = %v, want %v", c.launched, got, c.want)
		}
		//: a true return is a CLAIM, not a query: it must have consumed a slot,
		//: and a false one must have consumed none.
		wantHeld := c.held
		if got {
			wantHeld++
		}
		if len(h.slots) != wantHeld {
			t.Errorf("slots held = %d, want %d", len(h.slots), wantHeld)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hedge_release pins that a slot comes back however its duplicate ended. A
// leaked slot narrows the cap permanently: after enough hedged calls the policy
// would stop duplicating altogether, which looks exactly like a dependency that
// got fast again.
func Test_hedge_release(t *testing.T) {
	t.Parallel()
	h := &hedge{delay: tick, maxHedges: 1, slots: make(chan struct{}, 1)}
	for i := range 50 {
		if !h.acquire() {
			t.Fatalf("claim %d was refused — a slot leaked", i)
		}
		h.release()
	}
	if len(h.slots) != 0 {
		t.Errorf("slots held = %d, want 0", len(h.slots))
	}
}
