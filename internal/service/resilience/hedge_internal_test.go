// Package resilience — the hedging runner's race, its budget and its cap.
package resilience

import (
	"context"
	"errors"
	"math"
	"net/http"
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
	// slotWait bounds how long a test waits for a duplicate to hand its
	// in-flight slot back. It is a failure deadline, never a synchronisation
	// delay: the wait ends the instant the slot returns.
	slotWait time.Duration = 5 * time.Second
)

// errSlow and errFast identify which copy of an operation produced a failure;
// errPanicked is a sentinel an operation panics WITH, so a re-raise can be
// checked by identity rather than by message.
var (
	errSlow     = errors.New("the slow attempt failed")
	errFast     = errors.New("the fast attempt failed")
	errPanicked = errors.New("the operation panicked")
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

// Test_hedge_RunReRaisesAPanicWithItsOriginalValue pins that a panic inside a
// hedged Operation reaches the CALLER, and reaches it as itself.
//
// Hedging is the one policy that runs the Operation on goroutines of its own,
// and a panic that reaches the top of any goroutine ends the process. Under
// every other policy the same panic unwinds through the caller's goroutine,
// where net/http or the caller's recover contains it — so a hedge that let it
// escape turned one bad request into a dead binary.
//
// It is the ORIGINAL value that must arrive, compared with ==, not a wrapper
// that merely carries it: net/http recognises panic(http.ErrAbortHandler) by
// identity and aborts that handler silently, and a wrapped value would be
// logged as a crash instead. The stack is the documented cost — see NewHedge.
//
// MUTATION-CHECKED. Against the pre-fix code, where attempt ran op with no
// recover, the test binary itself dies — no test result, just:
//
//	panic: the operation panicked
//	goroutine N [running]:
//	…resilience.Test_hedge_RunReRaisesAPanicWithItsOriginalValue.func1.1(…)
//	…resilience.(*hedge).attempt(...)
//	created by …resilience.(*hedge).Run in goroutine M
//
// and re-raising a wrapper instead — panic(struct{ Raised any }{…}) in Run,
// the kernel/group shape — fails all three cases, the first with:
//
//	Run re-raised struct { Raised interface {} }{Raised:(*errors.errorString)(0x…)}, want the original value &errors.errorString{s:"the operation panicked"}
func Test_hedge_RunReRaisesAPanicWithItsOriginalValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the operation panics with.
		raised any
	}
	tests := []tc{
		{name: "a sentinel error arrives as itself", raised: errPanicked},
		{name: "net/http's abort sentinel keeps its identity", raised: http.ErrAbortHandler},
		{name: "a non-error payload arrives verbatim", raised: "the operation's own words"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: an hour-long delay issues no duplicate, so the panic is the FIRST
		//: attempt's — the copy that runs on every call.
		r := NewHedge(HedgeConfig{Idempotent: true, Delay: time.Hour, MaxHedges: 1, MaxInFlight: 1})

		ended := runRecovering(t.Context(), r, func(context.Context) error {
			panic(c.raised)
		})

		if !ended.panicked {
			t.Fatalf("Run returned %v instead of re-raising the operation's panic", ended.err)
		}
		if ended.raised != c.raised {
			t.Fatalf("Run re-raised %#v, want the original value %#v", ended.raised, c.raised)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hedge_RunDropsALosersLatePanic pins the other half: a copy that panics
// AFTER the race was decided must not reach anyone — and above all must not end
// the process.
//
// By then Run has returned, so there is no caller's goroutine left to re-raise
// the panic on. The only goroutine that still has it is the loser's own, and
// re-raising it there is precisely the crash this policy used to cause. It is
// recovered and dropped.
//
// The loser is a DUPLICATE on purpose: a duplicate hands its in-flight slot
// back only after its goroutine has finished — after the recover and the
// dropped hand-over — so the blocking send below completes exactly when that
// goroutine is done, with no sleep and no guess.
//
// MUTATION-CHECKED. Against the pre-fix code the test binary dies the moment
// the loser is released:
//
//	panic: the operation panicked
//	goroutine N [running]:
//	…resilience.Test_hedge_RunDropsALosersLatePanic.func1(…)
//	…resilience.(*hedge).attempt(...)
//	…resilience.(*hedge).duplicate(…)
//	created by …resilience.(*hedge).Run in goroutine M
//
// and recovering the panic but handing the outcome over with a plain send —
// no `case <-ctx.Done():` arm — fails it with:
//
//	the loser never handed its slot back within 5s — its goroutine is stuck
func Test_hedge_RunDropsALosersLatePanic(t *testing.T) {
	t.Parallel()
	h := &hedge{delay: tick, maxHedges: 1, slots: make(chan struct{}, 1)}
	duplicateStarted := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64

	err := h.Run(t.Context(), func(context.Context) error {
		//: the first copy wins, but only once the duplicate is in the race.
		if calls.Add(1) == 1 {
			<-duplicateStarted
			return nil
		}
		close(duplicateStarted)
		//: the duplicate ignores the cancellation and outlives the call it
		//: lost, then panics — the late panic this test is about.
		<-release
		panic(errPanicked)
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil — the first copy won", err)
	}

	close(release)
	//: the slot is full until the loser's goroutine returns it, which it does
	//: only after recovering the panic and dropping the outcome.
	select {
	case h.slots <- struct{}{}:
	case <-time.After(slotWait):
		t.Fatalf("the loser never handed its slot back within %s — its goroutine is stuck", slotWait)
	}
}

// Test_hedge_RunWithAnUnboundedDuplicateBudget pins that MaxHedges bounds how
// many duplicates a call may issue and nothing else — in particular not the
// memory a call allocates.
//
// The per-call result channel used to be buffered to maxHedges+1, so a budget
// of math.MaxInt overflowed and every single call panicked inside make(), and a
// merely large budget allocated, per call, a channel it could never fill. ADR
// 0031 has MaxHedges CLAMP rather than refuse, so every positive value is one a
// caller may legitimately write, and "as many as the delay and MaxInFlight
// allow" is exactly what math.MaxInt says.
//
// The second half is the property the sized buffer existed to buy, and the one
// the replacement must keep: a straggler that finishes after Run has returned
// neither blocks nor leaks. Its goroutine hands the in-flight slot back only
// once it has dropped its outcome, which is what the blocking send observes.
//
// MUTATION-CHECKED. The pre-fix code, and equally restoring
// make(chan attemptOutcome, h.maxHedges+1) on the fixed one, fail it with:
//
//	Run panicked with MaxHedges = math.MaxInt: makechan: size out of range
//
// and deleting attempt's `case <-ctx.Done():` arm — a plain send, which is
// what an unbuffered channel does to a straggler without it — fails it with:
//
//	the straggler never handed its slot back within 5s — it is blocked handing over an outcome nobody will read
func Test_hedge_RunWithAnUnboundedDuplicateBudget(t *testing.T) {
	t.Parallel()
	r := NewHedge(HedgeConfig{Idempotent: true, Delay: tick, MaxHedges: math.MaxInt, MaxInFlight: 1})
	h, ok := r.(*hedge)
	if !ok {
		t.Fatalf("NewHedge returned %T, want *hedge", r)
	}
	duplicateStarted := make(chan struct{})
	var calls atomic.Int64

	ended := runRecovering(t.Context(), r, func(ctx context.Context) error {
		//: the first copy wins once the duplicate is in the race, which makes
		//: the duplicate the straggler.
		if calls.Add(1) == 1 {
			<-duplicateStarted
			return nil
		}
		close(duplicateStarted)
		//: the straggler finishes only when the winner's cancellation reaches
		//: it — after Run has returned.
		<-ctx.Done()
		return ctx.Err()
	})
	if ended.panicked {
		t.Fatalf("Run panicked with MaxHedges = math.MaxInt: %v", ended.raised)
	}
	if ended.err != nil {
		t.Fatalf("Run = %v, want nil — the first copy won", ended.err)
	}

	select {
	case h.slots <- struct{}{}:
	case <-time.After(slotWait):
		t.Fatalf("the straggler never handed its slot back within %s — it is blocked handing over an outcome nobody will read", slotWait)
	}
}

// runRecovering runs op under r and reports how Run ended — the error it
// returned, or the value it panicked with — in the shape an attempt reports a
// copy's ending.
//
// It deliberately does not reuse attemptOutcome.capture: the observation a test
// relies on must not lean on the code the test is checking.
func runRecovering(ctx context.Context, r coreres.Runner, op coreres.Operation) attemptOutcome {
	var ended attemptOutcome
	func() {
		defer func() {
			ended.raised = recover()
			ended.panicked = ended.raised != nil
		}()
		ended.err = r.Run(ctx, op)
	}()
	return ended
}
