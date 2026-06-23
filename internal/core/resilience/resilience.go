// Package resilience declares the reliability port of the SDK: the context-aware
// Operation an executor guards and the composable Runner interface every policy
// (retry, circuit-breaker, rate-limit, bulkhead, timeout) satisfies. A core
// sibling admitted by ADR 0026 (Phase-B wave). Policies are concrete and live in
// internal/service/resilience; this package owns only the contract + the typed
// outcome sentinels, so policies compose by wrapping: Retry(Breaker(Timeout(op))).
package resilience

import "context"

// Operation is the unit of work a Runner guards. It MUST honour ctx
// cancellation (return promptly when ctx.Done fires) so timeout/cancellation
// propagate; a non-cancellable op caps a Timeout's precision at op granularity.
type Operation func(ctx context.Context) error

// Runner executes an Operation under a reliability policy and returns the
// operation's error, a policy sentinel (RetryExhausted, CircuitOpen,
// RateLimited, BulkheadFull, TimeoutExceeded), or ctx.Err(). Implementations
// MUST be safe for concurrent use. Runners compose: a Runner may itself invoke
// an inner Runner inside its Operation.
//
// IFACE-PLUGIN: concrete policies (retry/breaker/...) implement this interface;
// their concrete types stay unexported behind their constructors.
type Runner interface {
	Run(ctx context.Context, op Operation) error
}
