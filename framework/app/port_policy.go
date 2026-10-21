package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Retry runs a port's call again when it fails for a reason of the
// dependency's — what the start answers as a 5xx, a 408 or a 429 —, up to
// maxAttempts attempts in all, waiting between them a backoff that starts at
// delay and doubles, with full jitter, up to [RetryMaxDelay] — 30 seconds
// unless the code says otherwise. A failure that says how long to wait — a
// Retry-After, a rate limiter's next token (the SDK's resilience.RetryAfter)
// — is waited that long instead, held at the longest delay, unless
// [NoRetryAfter]. A refusal of the caller's own is not retried, nor an open
// breaker, a throttle or another retry's exhaustion. It is the SDK's
// resilience retry, on the app's clock, sharing the port's retry budget
// ([Budget]) with its hedge.
//
// Each retry is an event on the port's span, which counts them, and a
// retry event on the telemetry port.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-retry-attempts, -retry-delay, -retry-max-delay,
// -retry-jitter (full, decorrelated or none) and -retry-after (whether a
// failure's own delay is honoured), which config/<env>.* and
// <PRODUCT>_<NAME> override; fewer than one attempt, or a delay that is not
// positive, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Retry(maxAttempts int, delay time.Duration, opts ...RetryOption) PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Retry(maxAttempts, delay, opts...)
}

// RetryMaxDelay is the longest a [Retry] waits between two attempts: the
// backoff's cap, and the cap on a delay a failure asks for. It is the default
// of the port's setting <service>-port-<name>-retry-max-delay; one that is
// not positive stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RetryMaxDelay(d time.Duration) RetryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.RetryMaxDelay(d)
}

// NoRetryAfter makes a [Retry] wait its backoff whatever a failure says: a
// Retry-After, a rate limiter's next token. It is the default of the port's
// setting <service>-port-<name>-retry-after, false, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func NoRetryAfter() RetryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.NoRetryAfter()
}

// Budget sizes the retry budget a port's [Retry] and [Hedge] share: a
// bucket of maxTokens tokens, a failure costing one and a success putting
// back tokenRatio; retries and duplicates are refused once half the tokens
// are spent, so a dependency that fails for every caller receives fewer
// extra calls, not every call times the attempts. A port that retries or
// hedges without it has 10 tokens and a ratio of 0.1, gRPC's example; a
// Budget on a port that does neither stops the start. It is the SDK's
// resilience retry budget, one per port and per start.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-budget-tokens and -budget-ratio, which
// config/<env>.* and <PRODUCT>_<NAME> override; fewer than one token, or a
// ratio below a thousandth, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Budget(maxTokens int, tokenRatio float64) PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Budget(maxTokens, tokenRatio)
}

// Breaker stops a port's calls from reaching a dependency that keeps
// failing: after failures failures of the dependency's in a row — or, given
// [BreakerWindow], a share of a window's calls — it opens, and every call
// fails at once with the SDK's CircuitOpen (503) for open; then it lets
// [HalfOpenTrials] trial calls through, one at a time, which close it or open
// it again, for longer each time under [OpenBackoff]. A refusal of the
// caller's own does not count, nor a caller that left, nor a throttle's
// refusal. It is the SDK's resilience circuit breaker, on the app's clock,
// one per port and per start.
//
// Its state is the product's: a readiness check, breaker:<port>, degraded —
// never unready — while it is not closed; each change of phase logged and
// put on the telemetry port. An operator opens it until reset with the
// product's `breaker isolate PORT` and closes it with `breaker reset PORT`,
// kept in the data directory: a start applies it, and so does a running
// product, within five seconds.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-breaker-failures, -breaker-open, -breaker-open-max,
// -breaker-trials, -breaker-window, -breaker-window-calls and
// -breaker-window-ratio, which config/<env>.* and <PRODUCT>_<NAME> override;
// a value the breaker cannot run with stops the start, and so does a
// breaker the SDK refuses.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Breaker(failures int, open time.Duration, opts ...BreakerOption) PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Breaker(failures, open, opts...)
}

// BreakerWindow makes a [Breaker] open on a rate rather than on failures in
// a row: once the last size of calls holds at least minCalls, a share of
// failures of failureRatio or more opens it. Its values are the defaults of
// the port's settings <service>-port-<name>-breaker-window,
// -breaker-window-calls and -breaker-window-ratio; a window of 0 is none, a
// ratio outside (0, 1] stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func BreakerWindow(size time.Duration, minCalls int, failureRatio float64) BreakerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.BreakerWindow(size, minCalls, failureRatio)
}

// HalfOpenTrials is how many trial calls in a row must succeed to close a
// [Breaker] once its open time is over — one by default. It is the default
// of the port's setting <service>-port-<name>-breaker-trials; fewer than one
// stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func HalfOpenTrials(n int) BreakerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.HalfOpenTrials(n)
}

// OpenBackoff lengthens the open time of a [Breaker] that opens again
// without having closed: it doubles at each opening, from the breaker's open
// time up to max. It is the default of the port's setting
// <service>-port-<name>-breaker-open-max, 0 — the same open time every
// time — without it; a negative one stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OpenBackoff(maxOpen time.Duration) BreakerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OpenBackoff(maxOpen)
}

// Idempotent declares that the port's operation may run more than once for
// one call, concurrently, with no harm done: what a [Hedge] needs, which is
// refused on a port that does not say so. The graph draws it on the port.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Idempotent() PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Idempotent()
}

// Hedge races a port's slow call: an attempt that has not answered after
// delay is joined by a duplicate, and the first to succeed answers — the
// tail of a dependency's latency cut, at the price of more calls. At most
// inFlight duplicates run at once across every call, and the port's retry
// budget ([Budget]) refuses them while the dependency fails for many: then
// the call goes on with the attempts it has. Only a port declared
// [Idempotent] may hedge — the start refuses another. It is the SDK's
// resilience hedge, on the app's clock.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-hedge-delay, -hedge-in-flight and -hedge-max — the
// duplicates one call may make, 1 —, which config/<env>.* and
// <PRODUCT>_<NAME> override; a delay that is not positive, or fewer than
// one, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Hedge(delay time.Duration, inFlight int) PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Hedge(delay, inFlight)
}

// Throttle refuses, on the caller's side and without sending them, a share
// of a port's calls while the dependency refuses more than 1/k of the
// recent ones — the client-side throttling of Google's SRE book: a call is
// refused with probability (requests − k × accepts) / (requests + 1), over
// the last two minutes, with the SDK's Throttled (503). A refusal is a 5xx,
// a 408 or a 429 of the dependency's; a refusal of the caller's own is an
// accept. It is the SDK's resilience adaptive throttle, on the app's clock;
// 2 is the book's k.
//
// k is the default of the port's setting <service>-port-<name>-throttle-k,
// which config/<env>.* and <PRODUCT>_<NAME> override; a k below 1 stops the
// start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Throttle(k float64) PortPolicyOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Throttle(k)
}
