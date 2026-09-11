// Package resilience — fallback policy configuration.
package resilience

import coreres "github.com/kitsunium/sdk/internal/core/resilience"

// FallbackConfig parameterises NewFallback. A nil Fallback is refused (there is
// no defensible SDK-side substitute for the caller's own plan B); a nil
// Retryable falls back on every primary failure.
type FallbackConfig struct {
	// Fallback is the secondary Operation run when the primary one fails. It
	// has no default: a nil Fallback makes NewFallback return a policy that
	// refuses every call with PolicyMisconfigured (ADR 0031). The two values
	// the SDK could invent are both worse than a refusal — running the primary
	// and passing its error through is a policy that silently does nothing
	// while the caller believes their plan B is wired up, and a no-op fallback
	// that returns nil would report success for work that never happened.
	//
	// It is an Operation, not a func(ctx, primaryErr) error, so that the whole
	// policy vocabulary composes on the fallback side too: the plan B can
	// itself be a Runner stack (NewRetry(...).Run, a second region's client
	// behind its own breaker) with no adapter. What the fallback would have
	// learned from the primary error is decided ahead of it, by Retryable.
	//
	// The port carries no result value, so a "fallback VALUE" is expressed as
	// a closure assigning the default into the caller's own variable:
	//
	//	var page []byte
	//	cfg := FallbackConfig{Fallback: func(context.Context) error {
	//		page = cachedPage
	//		return nil
	//	}}
	Fallback coreres.Operation
	// Retryable classifies a non-nil PRIMARY error as one worth falling back
	// on. false returns that error verbatim without running the fallback: a
	// deterministic failure — a malformed request, an authorisation refusal,
	// a policy misconfiguration — is the caller's own, and serving a cached
	// answer for it hides a bug behind a stale success. A nil predicate falls
	// back on every failure. It shares its shape with RetryConfig.Retryable
	// and BreakerConfig.Retryable, so one classifier serves a nested
	// Fallback(Retry(Breaker(op))) stack.
	Retryable func(error) bool
}
