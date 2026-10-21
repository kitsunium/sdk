package resilience

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// adaptiveThrottle refuses calls on the client side with the probability the
// requests and accepts of its window give. Every field but window and random's
// state is set at construction; mu guards the window, and Random is called
// under it, so a caller's own source need not be safe for concurrent use.
type adaptiveThrottle struct {
	mu       sync.Mutex
	clk      clock.Clock
	rejected func(error) bool
	random   func() float64
	k        float64
	window   throttleWindow
}

// NewAdaptiveThrottle returns a Runner that refuses calls on the client
// side, before sending them, when the dependency has been refusing more
// than its share: the adaptive throttling of Google's SRE book ("Handling
// Overload"). Over the last cfg.Window it counts requests — every call
// Run was asked to make, the ones it refused included — and accepts —
// the calls the dependency answered without refusing them — and refuses
// each new call with probability
//
//	max(0, (requests − K × accepts) / (requests + 1))
//
// returning Throttled without running the operation. While the
// dependency accepts at least one call in K, nothing is refused; past
// that, the refusals grow with the excess. A dependency that refuses
// everything still receives a trickle of calls — the traffic that tells
// it, and us, when it has recovered — and the refusals fade as its
// accepts come back into the window.
//
// A call's error is a refusal when cfg.Rejected says so — every error
// when it is nil —; a nil error is an accept, and so is a call that
// ended in context.Canceled once the caller's own context was closed: a
// caller who left says nothing about the dependency. The refusal is drawn
// from cfg.Random, so a test decides it.
//
// A K below 1 or not a number, a negative Window, more than 1000
// Buckets, or a Window too short to split into its Buckets, is refused:
// every call returns PolicyMisconfigured, naming the field. A call that
// is not refused costs one lock and no allocation.
func NewAdaptiveThrottle(cfg AdaptiveThrottleConfig) coreres.Runner {
	settings, knob := resolveThrottle(cfg)
	//: a field out of range refuses the policy, by name (ADR 0031).
	if knob != "" {
		//: fail closed, and say why.
		return newMisconfigured(throttlePolicy, knob)
	}
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	random := cfg.Random
	//: a nil source is the standard library's, safe for concurrent use.
	if random == nil {
		random = rand.Float64
	}
	//: the window starts empty, so the first calls are never refused.
	return &adaptiveThrottle{
		clk: clk, rejected: cfg.Rejected, random: random, k: settings.k,
		window: newThrottleWindow(settings, clk.Now()),
	}
}

// Run refuses op with Throttled when the draw falls below the refusal
// probability, else runs it and counts whether the dependency accepted it.
func (t *adaptiveThrottle) Run(ctx context.Context, op coreres.Operation) error {
	//: refused on the client side: the operation is never run.
	if t.refuse() {
		//: the policy's own outcome.
		return wrapAs(coreres.Throttled, nil)
	}
	err := op(ctx)
	//: an accept is what brings the refusals back down.
	if t.accepted(ctx, err) {
		t.accept()
	}
	//: propagate the operation's own outcome.
	return err
}

// refuse counts one request and reports whether it is refused. The
// probability is read from the window BEFORE this request is counted, so the
// first request of an empty window is never refused. Holds mu.
func (t *adaptiveThrottle) refuse() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot := t.window.advance(t.clk.Now())
	p := t.window.refusal(t.k)
	//: a refused request is a request: it is what makes the refusals hold
	//: while the dependency keeps refusing.
	slot.requests++
	t.window.requests++
	//: no excess: no draw, so the healthy path never calls Random.
	if p <= 0 {
		//: admitted.
		return false
	}
	//: refused with probability p.
	return t.random() < p
}

// accepted reports whether a finished call counts as accepted by the
// dependency: a success, an error the classifier does not call a refusal, and
// a caller who left.
func (t *adaptiveThrottle) accepted(ctx context.Context, err error) bool {
	//: the dependency answered — or the caller cancelled and the operation
	//: said so, which is no word on the dependency, and must not throttle it.
	if err == nil || ctx.Err() != nil && errors.Is(err, context.Canceled) {
		//: accepted.
		return true
	}
	//: a nil classifier calls every error a refusal.
	if t.rejected == nil {
		//: refused.
		return false
	}
	//: the caller's classifier decides.
	return !t.rejected(err)
}

// accept counts one accept in the slot of now. Holds mu.
func (t *adaptiveThrottle) accept() {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot := t.window.advance(t.clk.Now())
	slot.accepts++
	t.window.accepts++
}
