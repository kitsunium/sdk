// Package resilience is the public facade for the SDK's reliability policies:
// retry, circuit-breaker, rate-limit, bulkhead, timeout, fallback, and hedging.
// Each constructor returns a [Runner] that guards an [Operation] (a ctx-aware
// func); policies compose by nesting.
//
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, OpenDuration: time.Second})
//	err := r.Run(ctx, func(ctx context.Context) error {
//	    return b.Run(ctx, callDownstream) // Retry(Breaker(op))
//	})
//
// Rejections surface typed sentinels (RetryExhausted / CircuitOpen / RateLimited
// / BulkheadFull / TimeoutExceeded / FallbackFailed) matchable via
// errs.HasReason / errs.HasCode.
//
// # Classifying deterministic failures
//
// Retry and the circuit-breaker act on transient failures. A deterministic one —
// a policy refusal, an HTTP 400, invalid input — gains nothing from a replay and
// says nothing about a dependency's health, yet by default both policies treat
// every non-nil error as transient: the retry spends its whole budget on it and
// hides it behind RetryExhausted, and the breaker counts it towards tripping.
// Set the Retryable predicate on either config to tell the two apart. It takes
// the same shape in both, so a nested Retry(Breaker(op)) shares one classifier.
// The sentinel is the caller's own — this package exports none to classify
// against, because only the caller knows which of their failures are
// deterministic:
//
//	var ErrBadRequest = errors.New("bad request") // declared by the caller
//
//	transient := func(err error) bool { return !errors.Is(err, ErrBadRequest) }
//	r := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: 3, Retryable: transient})
//	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 5, Retryable: transient})
//
// A rejected error comes back verbatim: the retry stops at that attempt without
// backoff and without the RetryExhausted relabel, and the breaker leaves its
// state machine untouched. A nil predicate keeps the historical behaviour.
// [FallbackConfig] takes the same predicate, where it decides whether a primary
// failure is worth serving a substitute answer for.
//
// # Falling back
//
// [NewFallback] runs a second Operation when the first one fails, and reports
// success when it works — that masking is the policy. The port carries no
// result value, so a "fallback VALUE" is a closure assigning the caller's own
// default:
//
//	var page []byte
//	f := resilience.NewFallback(resilience.FallbackConfig{
//	    Fallback: func(context.Context) error { page = cachedPage; return nil },
//	})
//	err := f.Run(ctx, func(ctx context.Context) error { return fetchLive(ctx, &page) })
//
// When BOTH halves fail, the result is FallbackFailed carrying both messages as
// fields — never one of the two errors alone. Reporting only the fallback's
// failure would never say what it was covering for, and reporting only the
// primary's would never say that plan B was tried and also broke; either way
// the outcome stops being diagnosable. A nil Fallback is refused (ADR 0031).
//
// A cancelled context is the one exception, and it is reported as itself: when
// it is already dead after the primary the fallback does not run at all, and
// when it dies while the fallback runs, a failed fallback returns ctx.Err()
// rather than FallbackFailed — neither dependency was at fault. A fallback that
// succeeds despite a late cancellation still returns nil: the work was done.
//
// # Hedging — for IDEMPOTENT operations only
//
// [NewHedge] guards tail latency by duplicating a slow call: when an attempt
// has been outstanding for Delay, a second copy starts, and the first to
// succeed wins.
//
//	h := resilience.NewHedge(resilience.HedgeConfig{
//	    Idempotent:  true,               // asserted, not assumed — see below
//	    Delay:       80 * time.Millisecond,
//	    MaxHedges:   1,
//	    MaxInFlight: 32,                 // duplicates in flight, all calls
//	})
//
// This is the only policy here that runs an Operation CONCURRENTLY with itself,
// which makes it the only one that can corrupt rather than merely delay: a
// non-idempotent Operation hedged is a double charge, a double insert, a
// duplicate outbound message — and the policy reports one clean success. The
// SDK cannot detect idempotence, so [HedgeConfig].Idempotent makes the claim
// mandatory and in code: its zero value refuses the policy, so hedging is never
// reached by copying a config and dropping a field, and `grep -r 'Idempotent:'`
// enumerates every hedged call path in a codebase.
//
// The second hazard is load. A dependency that has gone slow makes every
// in-flight call want a duplicate at the same instant, so hedging can deepen
// the outage it was meant to hide. Delay and MaxInFlight are both refused when
// unset for that reason — a zero Delay duplicates every call, and an unbounded
// MaxInFlight lets the duplicates scale with the outage. Reaching MaxInFlight
// degrades the policy to no-hedging (the call proceeds on its first attempt);
// it never turns into a rejection. Hedging also fires on latency ONLY: an
// attempt that fails before Delay elapses ends the call with that error, since
// replaying a failure is [NewRetry]'s job, and composing NewRetry(NewHedge(op))
// is how you get both.
//
// # Limiting per caller
//
// [NewRateLimiter] is one bucket for every caller, which is right for a
// dependency and wrong for an endpoint: one client guessing passwords empties
// it and locks everybody out. [NewKeyedRateLimiter] keeps a bucket per key —
// the authenticated user, the client's address — so one client is refused and
// the others are not:
//
//	limiter := resilience.NewKeyedRateLimiter(resilience.KeyedRateLimiterConfig{
//	    Rate: 1, Burst: 5,                  // per key
//	    Key:  func(ctx context.Context) string { return clientOf(ctx) },
//	    MaxKeys:     10_000,                // keys held at once, least recently used forgotten
//	    IdleTimeout: 10 * time.Minute,      // a key left alone this long starts over full
//	})
//
// The set of keys is bounded because the keys come from outside: a stream of
// distinct addresses would otherwise grow the process without limit. Both
// bounds are enforced on the calling goroutine — there is no sweeper. Key,
// MaxKeys and IdleTimeout have no defaults and are refused at zero, since each
// zero has a reading that disarms the limiter while every call keeps
// succeeding. A key pushed out by capacity comes back with a full bucket, so
// size MaxKeys above the number of clients active within IdleTimeout.
//
// # Backing off on your own terms
//
// [Backoff] is the curve [NewRetry] waits between attempts, published for the
// loops that retry on their own terms — a supervised goroutine restarting
// after a failure, an outbox rescheduling a delivery:
//
//	wait := resilience.Backoff{BaseDelay: time.Second, MaxDelay: time.Minute}.Delay(failures)
//
// Delay(n) is BaseDelay × Multiplier^(n−1), held at MaxDelay; a Multiplier of 1
// or below doubles, and the growth stops at the ceiling — or at the longest
// time.Duration — instead of wrapping negative, however large n is.
//
// # Driving the retry and the hedge with a test clock
//
// [RetryConfig].Clock is the time source the retry backs off on, and
// [HedgeConfig].Clock the one the hedge measures its delay on. Leave them nil
// in production; in a test, hand them a ManualClock from pkg/v1/clock and move
// it past each backoff, or past the hedge delay, instead of sleeping through
// it.
package resilience
