# ADR 0026 — Reliability domain (`resilience`)

- **Status**: Accepted
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0024 (Phase-B wave), ADR 0016 (proc — the no-registry core-sibling precedent), ADR 0005/0006 (error codes), ADR 0011 (clock)
- **Amends**: covered by the ADR 0024 Phase-B purpose-statement widening

## Context

Every downstream re-implements retry/backoff, circuit-breakers, rate-limiting,
bulkheads, and timeouts by hand. These are generic reliability primitives that
fit the SDK's ethos (clock-injectable, typed errors). Phase B adds them as the
`resilience` domain.

## Decision

1. **Add `internal/core/resilience`** — a core sibling declaring `Operation
   func(ctx) error` and the composable `Runner interface { Run(ctx, op) error }`,
   plus the typed outcome sentinels. **No registry** (policies are concrete
   algorithms, not pluggable schemes — the `proc` precedent, ADR 0016).
2. **`internal/service/resilience`** ships five concrete policies, each a
   `Runner`: `NewRetry` (capped exponential backoff, ctx-aware), `NewCircuitBreaker`
   (Closed→Open→HalfOpen, injectable clock), `NewRateLimiter` (token bucket,
   reject mode), `NewBulkhead` (channel-semaphore, reject mode), `NewTimeout`
   (`context.WithTimeout`). Policies **compose by nesting**:
   `Retry(Breaker(Timeout(op)))`.
3. **`pkg/v1/resilience`** aliases the port + `*Config` types and re-exports the
   constructors + sentinels.
4. **Error block `0.2.8.*`**: `RETRY_EXHAUSTED`, `CIRCUIT_OPEN`, `RATE_LIMITED`,
   `BULKHEAD_FULL`, `TIMEOUT_EXCEEDED` (all `EX_TEMPFAIL` = transient). Defined in
   core; service emits them via `wrapAs` (sentinel origin-wins, cause as a field)
   so the policy code survives even an `*errs.Error` cause.

### Cross-platform (ADR 0018)

100 % portable Go (context, time, sync, atomic). No OS-specific code; trivial
build bar on all 8 GOOS.

## Consequences

- 8th core sibling (no registry, like proc). `pkg/v1` gains a dep-light facade.
  Docs + `docs/error-codes.yaml` updated per rule 11.
- `Operation` carries `ctx`; an op that ignores ctx caps Timeout precision at op
  granularity (documented). Retry sleeps with a real timer (tests use BaseDelay=0).

## Alternatives considered

- **Logger-middleware-only** (extend `service/logger/middleware`) — rejected:
  resilience is a cross-cutting concern, not a logging one; it deserves a domain.
- **A registry of policies** — rejected: there is one retry algorithm, one
  breaker, etc.; a registry is over-abstraction (the proc no-registry precedent).
- **`func() error` (no ctx)** — rejected: context is required for honest
  timeout/cancellation propagation.

## Deferred

- Retry jitter + a retryable-error classifier.
- Wait-mode (blocking) bulkhead/rate-limiter (v1 is reject-mode).
- Half-open concurrency control (v1 admits HalfOpen trials without a probe cap).

## References

- Impl: `internal/core/resilience/`, `internal/service/resilience/`, `pkg/v1/resilience/`.
- ADR 0016 (no-registry sibling), ADR 0024 (Phase-B wave), ADR 0018 (portability).
