// Package resilience — range 0.2.8.* (ADR 0026 core/app/resilience block).
//
// Package resilience — declares the sentinel *errs.Error policy outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package resilience declares the reliability port of the SDK: the context-aware
// Operation an executor guards and the composable Runner interface every policy
// (retry, circuit-breaker, rate-limit, bulkhead, timeout, fallback, hedging)
// satisfies. A core sibling admitted by ADR 0026 (Phase-B wave). Policies are
// concrete and live in internal/service/app/resilience; this package owns only the
// contract + the typed outcome sentinels, so policies compose by wrapping:
// Retry(Breaker(Timeout(op))).
package resilience
