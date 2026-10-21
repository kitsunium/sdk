package resilience

import (
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BreakerConfig parameterises NewCircuitBreaker. A non-positive FailureThreshold
// defaults to 5; a non-positive OpenDuration defaults to 30s; a nil Clock
// defaults to clock.System; a nil Retryable counts every error as a failure;
// a nil Window opens on consecutive failures; a nil OnStateChange hears
// nothing; a nil OpenBackoff keeps every opening at OpenDuration; a
// non-positive HalfOpenTrials closes on one successful trial.
type BreakerConfig struct {
	// FailureThreshold is how many consecutive failures open a breaker
	// without a Window. A non-positive value defaults to 5. It is not read
	// when Window is set.
	FailureThreshold int
	// OpenDuration is how long an Open breaker rejects calls before admitting
	// one HalfOpen trial. A non-positive value clamps to 30s: a zero cooldown
	// would let the very next call through, which is a breaker that never
	// rejects anything and so protects nothing while the caller believes it
	// does (ADR 0031).
	OpenDuration time.Duration
	Clock        clock.Clock
	// Retryable classifies a non-nil operation error as a dependency failure
	// worth counting against the breaker's health. false means the error is
	// deterministic — the caller's own fault, not the dependency's — so it is
	// returned verbatim and folded into neither the failure count nor the
	// success path: a stream of HTTP 400s can no longer trip the breaker, and
	// one of them cannot close a HalfOpen breaker either. A nil predicate
	// counts every error as a failure, which is the pre-classifier behaviour.
	// It is not consulted for a context.Canceled returned once the caller's
	// context is closed: that call is neutral whatever the predicate says.
	// Shares its shape with RetryConfig.Retryable so one classifier serves a
	// nested Retry(Breaker(op)) stack.
	Retryable func(error) bool
	// Window, when set, opens the breaker on a RATE rather than a streak:
	// of failures, or of slow calls, among the calls a sliding time window
	// holds. Nil — the zero value — is the consecutive-failure breaker,
	// exactly as it was before the field existed.
	//
	// A streak misses a dependency that fails every other call: S, F, S,
	// F never reaches a threshold of five, and neither does a dependency
	// that answers every call, eventually, a minute late. A rate over a
	// window sees both. The window's fields have defaults of their own,
	// taken from Polly and resilience4j — see BreakerWindowValue.
	Window *BreakerWindowValue
	// OnStateChange, when set, is called on every change of phase, with the
	// phase left and the phase entered: Closed to Open on a trip, Open to
	// HalfOpen when a call is admitted as the trial, HalfOpen to Closed or
	// Open on its outcome, and whatever Isolate and Reset change.
	//
	// It is called outside the breaker's lock, one call at a time and in
	// the order the changes happened, on the goroutine of the call that
	// made the change — or of one already delivering an earlier change,
	// which then delivers this one too. It may call State, Isolate or
	// Reset. It must not block: a call that changed the phase waits for
	// it. Nil hears nothing.
	OnStateChange func(from coreres.BreakerPhase, to coreres.BreakerPhase)
	// OpenBackoff, when set, makes each opening last longer than the one
	// before while the dependency keeps failing its trials: the n-th
	// opening since the breaker last closed lasts OpenBackoff.Delay(n),
	// in place of OpenDuration. The count starts over when a trial closes
	// the breaker, and on Reset.
	//
	// Its BaseDelay and its MaxDelay are both required — a zero base is
	// a cooldown of nothing, and a zero ceiling one that may grow to
	// centuries — and are refused when not positive, with
	// PolicyMisconfigured naming the field. Its Multiplier, Jitter and
	// JitterMode read as BackoffValue reads them. Nil keeps every opening
	// at OpenDuration.
	OpenBackoff *BackoffValue
	// HalfOpenTrials is how many trials in a row must succeed before a
	// HalfOpen breaker closes. The trials still run one at a time, and
	// any one that fails opens the breaker again. Zero, or less, is one:
	// the first successful trial closes it, as before the field existed.
	//
	// One success is thin evidence that a dependency has recovered — the
	// trial may have reached the one healthy replica, or a cache — and a
	// breaker that closes on it and trips again at once doubles the load
	// it meant to spare. A few trials in a row cost a few more cooldowns'
	// worth of latency to the first callers after recovery, which is what
	// resilience4j's permittedNumberOfCallsInHalfOpenState buys too.
	HalfOpenTrials int
}

// BreakerWindowValue is a circuit breaker's sliding window: the calls of the
// last Size, kept in Buckets slots of Size/Buckets each on the breaker's
// clock, the oldest slot dropped as time moves past it. The breaker opens
// when the window holds at least MinCalls calls and either FailureRatio of
// them failed or SlowRatio of them lasted SlowAfter or longer.
//
// A zero field takes its default, each one a published library's own:
// Size 30s, Buckets 10, MinCalls 100 and FailureRatio 0.1 are Polly v8's
// SamplingDuration, its ten sampling windows, MinimumThroughput and
// FailureRatio; SlowAfter 60s and SlowRatio 1 are resilience4j's
// slowCallDurationThreshold and slowCallRateThreshold. A negative value,
// a ratio above 1 or not a number, more than 1000 Buckets, or a Size too
// short to split into Buckets, is refused: every call returns
// PolicyMisconfigured, naming the field.
//
// A call is counted when it ends, in the slot of its end, if the breaker
// is still Closed and it was not admitted before the breaker last opened. A
// deterministic error and a caller who left are not counted at all. The
// window is emptied each time the breaker changes phase.
type BreakerWindowValue struct {
	// Size is how far back the window reaches. Zero is 30 seconds.
	Size time.Duration
	// Buckets is how many slots the window is kept in: a slot leaves the
	// window whole, so the window reaches back between Size − Size/Buckets
	// and Size. Zero is 10; more than 1000 is refused, the memory of a
	// breaker being one slot each.
	Buckets int
	// MinCalls is how many calls the window must hold before a ratio is
	// read at all: below it, a single failure would be a rate of 100%.
	// Zero is 100.
	MinCalls int
	// FailureRatio is the share of failed calls, in (0, 1], at or above
	// which the breaker opens. Zero is 0.1.
	FailureRatio float64
	// SlowAfter is the duration, measured on the breaker's clock from
	// admission to return, at or above which a call is slow, whether it
	// succeeded or failed. Zero is 60 seconds.
	SlowAfter time.Duration
	// SlowRatio is the share of slow calls, in (0, 1], at or above which
	// the breaker opens. Zero is 1: only a window where every call was
	// slow opens it.
	SlowRatio float64
}
