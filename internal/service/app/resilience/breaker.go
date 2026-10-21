package resilience

import (
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// defaultThreshold is the consecutive-failure count that opens the breaker when
// none is configured.
const defaultThreshold int = 5

// defaultOpenDuration is the cooldown an Open breaker serves when none is
// configured. A zero cooldown lets the very next call re-probe the downstream,
// which is a breaker that rejects nothing — the caller believes it is protected
// and is not (ADR 0031). Thirty seconds is the same order of arbitrary as the
// five-failure defaultThreshold above and is the industry-conventional range.
const defaultOpenDuration time.Duration = 30 * time.Second

// breakerPolicy names the breaker in a PolicyMisconfigured refusal.
const breakerPolicy string = "breaker"

// changesCapacity is the room a breaker with an OnStateChange keeps for the
// changes of phase not yet delivered: a trip, a trial and its outcome fit
// without growing it.
const changesCapacity int = 4

// circuitBreaker trips Open after FailureThreshold consecutive failures — or,
// with a window, on a rate of failures or of slow calls —, rejects for its
// cooldown, then half-opens for ONE trial at a time.
//
// generation counts the trips, the isolations and the resets. A call captures
// it on admission and its outcome is folded in only while it is still
// current: a call admitted before the breaker last opened is a straggler, and
// its result describes a dependency the breaker has already judged — its
// success must not close an Open breaker and its failure must not restart a
// cooldown it did not cause.
//
// cooldown is the configured OpenDuration; openFor is the cooldown of the
// current opening, which OpenBackoff lengthens with opens, the openings since
// the breaker last closed. queue holds the changes of phase OnStateChange has
// not heard yet, handed out outside mu, one at a time, in order.
type circuitBreaker struct {
	mu         sync.Mutex
	clk        clock.Clock
	retryable  func(error) bool
	onChange   func(from coreres.BreakerPhase, to coreres.BreakerPhase)
	window     *slidingWindow
	threshold  int
	trials     int
	passed     int
	cooldown   time.Duration
	backoff    *kbackoff.Value
	state      breakerState
	isolated   bool
	failures   int
	opens      int
	trips      uint64
	openedAt   time.Time
	openFor    time.Duration
	generation uint64
	probing    bool
	queue      changeQueue
}

// phaseChange is one change of phase OnStateChange is owed.
type phaseChange struct {
	from coreres.BreakerPhase
	to   coreres.BreakerPhase
}

// admission is what allow hands Run: the generation the call was admitted in,
// whether it is the HalfOpen trial, the one call that may close the breaker,
// and whether this goroutine must deliver the changes of phase allow queued.
type admission struct {
	generation uint64
	trial      bool
	deliver    bool
}

// outcome is what a finished call tells the state machine.
type outcome int

const (
	// outcomeNeutral is a deterministic error, an abandoned call, or a call the
	// caller cancelled: no signal.
	outcomeNeutral outcome = iota
	// outcomeSuccess is a call that returned nil.
	outcomeSuccess
	// outcomeFailure is a call whose error the classifier counts.
	outcomeFailure
)

// NewCircuitBreaker returns a Runner guarding op with a Closed→Open→HalfOpen
// breaker. Open calls return CircuitOpen until OpenDuration elapses; a
// non-positive OpenDuration clamps to defaultOpenDuration, because a breaker
// whose cooldown is zero admits the next call immediately and therefore
// protects nothing (ADR 0031). An error rejected by cfg.Retryable is returned
// verbatim and left out of the state machine entirely, so deterministic
// failures cannot trip the breaker. Once the cooldown elapses ONE HalfOpen
// trial runs at a time and every other call returns CircuitOpen until it
// settles; cfg.HalfOpenTrials successful trials in a row close the
// breaker, and any failed one opens it again. The outcome of a call
// admitted before the breaker last opened is ignored: its success cannot
// close the breaker and its failure cannot restart the cooldown.
//
// A call that ends in context.Canceled once the caller's own context is
// closed is neutral — neither a success nor a failure — because the caller
// left, not the dependency: a wave of client disconnects cannot open the
// breaker on a healthy dependency, and a HalfOpen trial abandoned that way
// frees the slot for the next call. context.DeadlineExceeded stays a
// failure: a dependency too slow for the caller's deadline is a slow
// dependency.
//
// With a nil cfg.Window the breaker opens on FailureThreshold consecutive
// failures. With one, it opens on the rate of failures, or of slow calls,
// among the calls of a sliding time window on cfg.Clock, once that window
// holds at least MinCalls of them; a HalfOpen trial that lasts SlowAfter
// or longer counts as failed. A Window or an OpenBackoff with a value it
// cannot honour is refused: every call returns PolicyMisconfigured,
// naming the field, and the Runner returned is not a BreakerState.
//
// The Runner returned otherwise implements BreakerState: State reports
// the phase and its counts, Isolate opens the breaker until Reset, and
// Reset closes it. cfg.OnStateChange hears every change of phase.
func NewCircuitBreaker(cfg BreakerConfig) coreres.Runner {
	//: default the clock to the system source.
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	//: a non-positive threshold falls back to the default.
	threshold := cfg.FailureThreshold
	//: clamp a non-positive threshold to the 5-failure default.
	if threshold < 1 {
		//: standard 5-failure trip.
		threshold = defaultThreshold
	}
	//: a non-positive cooldown falls back to the default.
	//: one successful trial closes the breaker unless more are asked for.
	trials := max(cfg.HalfOpenTrials, 1)
	openFor := cfg.OpenDuration
	//: clamp it like every sibling knob — a zero cooldown is not a breaker.
	if openFor <= 0 {
		//: standard 30-second cooldown.
		openFor = defaultOpenDuration
	}
	//: a fresh breaker starts Closed.
	b := &circuitBreaker{
		clk: clk, retryable: cfg.Retryable, onChange: cfg.OnStateChange,
		threshold: threshold, trials: trials, cooldown: openFor, openFor: openFor, state: coreres.BreakerClosed,
	}
	//: the two optional parts refuse what they cannot honour.
	if knob := b.configure(&cfg); knob != "" {
		//: fail-closed, naming the field (ADR 0031).
		return newMisconfigured(breakerPolicy, knob)
	}
	//: ready to guard calls.
	return b
}

// configure installs the open backoff, the window and the change queue cfg
// asks for, and returns the field it refuses, if any.
func (b *circuitBreaker) configure(cfg *BreakerConfig) string {
	//: a growing cooldown needs a base and a ceiling.
	if cfg.OpenBackoff != nil {
		//: a zero base is a cooldown of nothing.
		if cfg.OpenBackoff.BaseDelay <= 0 {
			//: refused.
			return "OpenBackoff.BaseDelay"
		}
		//: a zero ceiling lets the cooldown grow to centuries.
		if cfg.OpenBackoff.MaxDelay <= 0 {
			//: refused.
			return "OpenBackoff.MaxDelay"
		}
		//: a copy: the caller's value may change after construction.
		curve := *cfg.OpenBackoff
		b.backoff = &curve
	}
	//: a window replaces the consecutive count.
	if cfg.Window != nil {
		settings, knob := resolveWindow(*cfg.Window)
		//: a field out of range refuses the breaker.
		if knob != "" {
			//: refused.
			return knob
		}
		b.window = newWindow(settings, b.clk.Now())
	}
	//: room for the changes a listener is owed, allocated once.
	if b.onChange != nil {
		b.queue.changes = make([]phaseChange, 0, changesCapacity)
	}
	//: every field honoured.
	return ""
}
