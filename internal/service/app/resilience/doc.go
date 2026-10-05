// Package resilience — the exponential backoff every waiting policy computes,
// re-exported from the kernel package that owns it.
//
// Package resilience — circuit-breaker policy.
//
// Package resilience — circuit-breaker configuration.
//
// Package resilience — circuit-breaker state machine values.
//
// Package resilience — bulkhead (bounded-concurrency) policy.
//
// Package resilience — fallback (secondary-operation) policy.
//
// Package resilience — fallback policy configuration.
//
// Package resilience — hedging (duplicate-request racing) policy.
//
// Package resilience — hedging policy configuration.
//
// Package resilience — what one hedged attempt reports back to the race.
//
// Package resilience — the per-call bookkeeping of a hedged race.
//
// Package resilience — the keyed rate limiter: one token bucket per caller,
// with the set of callers bounded and idle ones forgotten.
//
// Package resilience — keyed rate-limiter configuration.
//
// Package resilience — the Runner returned when a policy cannot be honoured.
//
// Package resilience — token-bucket rate-limit policy.
//
// Package resilience — token-bucket rate-limiter configuration.
//
// Package resilience — retry-with-exponential-backoff policy.
//
// Package resilience — retry policy configuration.
//
// Package resilience — shared retryable-error classification helper.
//
// Package resilience provides the concrete reliability policies (retry,
// circuit-breaker, rate-limit, bulkhead, timeout, fallback, hedging)
// implementing core/app/resilience.Runner. Each constructor returns a Runner;
// policies compose by nesting. ADR 0026. Cross-OS: 100% portable (context,
// time, sync, atomic).
//
// Hedging is the one policy that runs the Operation CONCURRENTLY with itself,
// so it is correct only on an idempotent Operation; HedgeConfig.Idempotent
// makes the caller say so in code (ADR 0031's rule applied to a precondition
// the SDK cannot check).
//
// Package resilience — shared sentinel-wrapping helper.
package resilience
