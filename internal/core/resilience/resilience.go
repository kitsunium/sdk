// Package resilience declares the reliability port of the SDK: the context-aware
// Operation an executor guards and the composable Runner interface every policy
// (retry, circuit-breaker, rate-limit, bulkhead, timeout, fallback, hedging)
// satisfies. A core sibling admitted by ADR 0026 (Phase-B wave). Policies are
// concrete and live in internal/service/resilience; this package owns only the
// contract + the typed outcome sentinels, so policies compose by wrapping:
// Retry(Breaker(Timeout(op))).
package resilience

import "context"

// Operation is the unit of work a Runner guards. It MUST honour ctx
// cancellation (return promptly when ctx.Done fires) so timeout/cancellation
// propagate; a non-cancellable op caps a Timeout's precision at op granularity.
//
// The port says nothing about idempotence, because most policies do not need
// it: retry replays an Operation only after it has failed, and every other
// policy runs it at most once. The hedging policy is the exception — it runs
// copies CONCURRENTLY, so a non-idempotent Operation produces a double effect
// (a double charge, a double insert) rather than a slow one. The SDK cannot
// detect idempotence, so the hedging config makes the caller assert it in code
// (HedgeConfig.Idempotent) instead of trusting a doc comment.
type Operation func(ctx context.Context) error

// Runner executes an Operation under a reliability policy and returns the
// operation's error, a policy sentinel (RetryExhausted, CircuitOpen,
// RateLimited, BulkheadFull, TimeoutExceeded, FallbackFailed), or ctx.Err().
// Implementations MUST be safe for concurrent use. Runners compose: a Runner
// may itself invoke an inner Runner inside its Operation.
//
// IFACE-PLUGIN: concrete policies (retry/breaker/...) implement this interface;
// their concrete types stay unexported behind their constructors.
type Runner interface {
	Run(ctx context.Context, op Operation) error
}
