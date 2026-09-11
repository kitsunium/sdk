// Package resilience — hedging (duplicate-request racing) policy.
package resilience

import (
	"context"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// minHedges is the floor on the duplicate budget of a single call: a hedging
// policy that may issue no duplicate is not a hedging policy.
const minHedges int = 1

// minInFlight is the smallest in-flight duplicate cap that still hedges; below
// it the knob is refused rather than clamped (see HedgeConfig.MaxInFlight).
const minInFlight int = 1

// hedge races duplicate copies of an Operation against the original once it has
// been outstanding for delay, and returns the first success. slots bounds the
// number of duplicates in flight across every concurrent call.
type hedge struct {
	delay     time.Duration
	maxHedges int
	slots     chan struct{}
}

// NewHedge returns a Runner that guards tail latency by DUPLICATING a slow
// Operation: when the first attempt has been outstanding for cfg.Delay, a
// second copy starts, and so on every cfg.Delay up to cfg.MaxHedges extra
// copies. The first attempt to succeed wins and Run returns nil; the losers'
// context is cancelled on the way out and their results are discarded.
//
// # The Operation MUST be idempotent
//
// Every other policy here runs the Operation at most once at a time; this one
// runs copies CONCURRENTLY, so a non-idempotent Operation is not made slow by
// hedging, it is made WRONG — two charges, two inserts, two outbound messages,
// with the policy reporting one clean success. The SDK cannot detect
// idempotence, so cfg.Idempotent is a mandatory in-code assertion rather than a
// warning in prose: its zero value refuses the policy (ADR 0031).
//
// # Hedging acts on latency, never on failure
//
// An attempt that FAILS before cfg.Delay elapses ends the call with that error,
// verbatim; no duplicate is issued. Reacting to a failure by launching another
// attempt is retrying, and this package already has NewRetry — compose
// NewRetry(NewHedge(op)) to get both. Keeping them separate is also what keeps
// the duplication bounded by latency rather than by failure: a dependency that
// is failing fast gets no extra traffic from this policy, only a slow one does.
//
// # Load
//
// cfg.MaxInFlight caps the duplicates in flight across all calls, so a
// dependency that has gone slow cannot make every caller hedge at once. When
// the cap is reached the duplicate is simply not issued and the call proceeds
// on its first attempt alone — degrading to no-hedging, never to a rejection.
//
// # A panic reaches the caller, with its original value
//
// This is the one policy that runs the Operation on goroutines of its own, and
// a panic that reaches the top of any goroutine ends the process — where under
// every other policy the same panic reaches the caller's goroutine and is
// contained by net/http or by the caller's own recover. So a copy's panic is
// recovered on that copy's goroutine and re-raised from Run, on the caller's
// goroutine, with the Operation's ORIGINAL value: the caller's recover compares
// equal to exactly what was panicked with, and http.ErrAbortHandler still
// aborts a handler silently instead of being logged as a crash.
//
// What does NOT survive is the stack. The re-raised panic's stack is the
// re-raise site inside Run, not the Operation's frame: a value re-raised on
// another goroutine cannot carry the panicking goroutine's stack without being
// wrapped, and wrapping it would break both http.ErrAbortHandler and value
// equality. A copy that panics after Run has returned — a loser, once a winner
// was found or the caller went away — is recovered and dropped; it never
// reaches the caller and never ends the process.
//
// Run costs one goroutine per attempt (including the first, so the delay stays
// observable) and one ticker per call: this is the one policy in the package
// that is not allocation-trivial, which is the price of racing. Neither cost
// grows with cfg.MaxHedges, which bounds how many duplicates a call may issue
// and nothing else.
//
// A false cfg.Idempotent, a non-positive cfg.Delay, or a non-positive
// cfg.MaxInFlight is refused: every call returns PolicyMisconfigured without
// running the operation (ADR 0031). A cfg.MaxHedges below one clamps to one.
func NewHedge(cfg HedgeConfig) coreres.Runner {
	//: the precondition the SDK cannot check itself, and whose breach is a
	//: silent double effect rather than an error — so it is asserted in code.
	if !cfg.Idempotent {
		//: fail closed, and say why.
		return newMisconfigured("hedge", "Idempotent")
	}
	//: a zero delay duplicates every call the instant it starts, which is a
	//: load amplifier rather than a resilience policy; the useful value is the
	//: caller's measured latency distribution, which the SDK cannot guess.
	if cfg.Delay <= 0 {
		//: fail closed, and say why.
		return newMisconfigured("hedge", "Delay")
	}
	//: an unbounded duplicate budget adds load exactly when the dependency has
	//: least to spare, and an SDK-chosen bound would instead stop hedging under
	//: the very load hedging was bought for. Neither guess is safe — refuse.
	if cfg.MaxInFlight < minInFlight {
		//: fail closed, and say why.
		return newMisconfigured("hedge", "MaxInFlight")
	}
	//: unlike the three above, "issue at least one duplicate" is an obvious
	//: floor — a zero budget would make the policy inert, so it clamps.
	maxHedges := max(cfg.MaxHedges, minHedges)
	//: the buffered channel's capacity is the in-flight duplicate bound.
	return &hedge{delay: cfg.Delay, maxHedges: maxHedges, slots: make(chan struct{}, cfg.MaxInFlight)}
}

// Run races duplicate attempts and returns the first success, or — when every
// attempt it launched has failed — the first error it received, verbatim.
//
// A copy that PANICS is re-raised here, on the caller's goroutine, with the
// value it panicked with — see NewHedge for what that preserves and what it
// cannot.
//
// Goroutine lifecycle: one goroutine per attempt (the first, plus up to
// maxHedges duplicates), all started here and all owned by this call. Each
// hands its outcome to this loop over an unbuffered channel or, once the
// attempts' shared context is done, drops it and exits — so a loser that
// outlives Run neither blocks nor leaks, and a loser's late panic ends there
// instead of ending the process. The deferred cancel is what tells every one
// of them to stop the moment a winner is known.
func (h *hedge) Run(ctx context.Context, op coreres.Operation) error {
	//: one cancellable context shared by every attempt, so the losers are told
	//: to stop the moment a winner is known (deferred cancel covers every exit).
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	//: unbuffered, and deliberately NOT sized to the duplicate budget: an
	//: attempt hands its outcome over or, once actx is done, drops it (see
	//: attempt), so no straggler ever needs a slot to deposit into. Sizing it
	//: maxHedges+1 overflowed into a make() panic on every call at a budget of
	//: math.MaxInt, and made a merely large budget allocate, per call, a
	//: channel the call could never fill.
	results := make(chan attemptOutcome)
	//: the first attempt runs in a goroutine too — Run must stay free to watch
	//: the delay elapse while that attempt is outstanding.
	go h.attempt(actx, op, results)
	//: one tick per delay: a duplicate is issued only while nothing has come
	//: back, which is what keeps the added load proportional to the slowness.
	ticker := time.NewTicker(h.delay)
	defer ticker.Stop()
	//: the first attempt is already in the race.
	race := hedgeRace{launched: 1}
	//: run until a winner, a total failure, or the caller's cancellation.
	for {
		select {
		case <-ctx.Done():
			//: the caller went away; the deferred cancel stops the attempts.
			return ctx.Err()
		case outcome := <-results:
			//: a copy that panicked is a fault, not an outcome, and it outranks
			//: every error: re-raised on the caller's goroutine with the value
			//: it carried, exactly where any other policy would have let it
			//: surface. The deferred cancel still stops the other copies.
			if outcome.panicked {
				panic(outcome.raised)
			}
			//: an undecided race keeps waiting on the copies still outstanding.
			if decided, verdict := race.record(outcome.err); decided {
				//: the race is over.
				return verdict
			}
		case <-ticker.C:
			//: the attempt has been slow enough to be worth duplicating — if
			//: this call still has budget and the Runner-wide cap has room.
			if !h.claimHedge(race.launched) {
				//: no duplicate this tick; keep waiting on the ones in flight.
				continue
			}
			//: one more copy in the race.
			race.launched++
			//: the duplicate holds its slot for as long as it actually runs.
			go h.duplicate(actx, op, results)
		}
	}
}

// claimHedge claims one in-flight slot for a call that has already launched the
// given number of attempts, reporting whether the duplicate may be issued. It
// is a claim, not a query: a true return has consumed a slot, which duplicate
// releases when the copy ends.
func (h *hedge) claimHedge(launched int) bool {
	//: launched counts the first attempt too, so the budget is spent once it
	//: exceeds the duplicate allowance.
	if launched > h.maxHedges {
		//: this call has issued every duplicate it is allowed.
		return false
	}
	//: the Runner-wide cap is what stops a slow dependency from being hedged by
	//: every caller at once.
	return h.acquire()
}

// attempt runs one copy of op and hands its outcome to Run.
//
// The copy runs under attemptOutcome.capture, which recovers a panic on THIS
// goroutine — the only place it can be recovered. Every other policy runs the
// Operation on the caller's goroutine, where net/http or the caller's own
// recover contains a panic; here the same panic, left alone, ended the process.
func (h *hedge) attempt(ctx context.Context, op coreres.Operation, out chan<- attemptOutcome) {
	var outcome attemptOutcome
	outcome.capture(ctx, op)
	select {
	//: Run is still deciding the race and takes the outcome.
	case out <- outcome:
	//: the attempts' context is done: Run has returned with a winner, or the
	//: caller went away and Run is returning that instead — either way nobody
	//: will read this. Dropping it is what lets a straggler exit rather than
	//: block forever, and it is where a loser's late panic ends: there is no
	//: goroutine left to re-raise it on except this one, and re-raising it
	//: here would end the process.
	case <-ctx.Done():
	}
}

// duplicate runs one hedged copy of op, holding its in-flight slot for as long
// as the copy actually runs.
func (h *hedge) duplicate(ctx context.Context, op coreres.Operation, out chan<- attemptOutcome) {
	//: the slot is held for the whole duplicate call, not until Run returns: a
	//: loser that has not yet noticed the cancellation is still consuming the
	//: downstream, and the cap exists to count exactly that.
	defer h.release()
	//: same body as the first attempt — the only difference is the accounting.
	h.attempt(ctx, op, out)
}

// acquire claims one in-flight duplicate slot without blocking.
func (h *hedge) acquire() bool {
	//: never wait for a slot — waiting would add the latency the policy exists
	//: to remove.
	select {
	case h.slots <- struct{}{}:
		//: claimed.
		return true
	default:
		//: every slot taken — this tick issues no duplicate.
		return false
	}
}

// release returns an in-flight duplicate slot to the pool.
func (h *hedge) release() {
	//: one receive per successful acquire.
	<-h.slots
}
