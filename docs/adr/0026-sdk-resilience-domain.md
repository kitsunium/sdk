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

## Why not

- **Logger-middleware-only** (extend `service/logger/middleware`) — rejected:
  resilience is a cross-cutting concern, not a logging one; it deserves a domain.
- **A registry of policies** — rejected: there is one retry algorithm, one
  breaker, etc.; a registry is over-abstraction (the proc no-registry precedent).
- **`func() error` (no ctx)** — rejected: context is required for honest
  timeout/cancellation propagation.

## Breaking changes

None. `resilience` is a new domain in this change set — there is no prior
published surface to break.

Two contracts were tightened during review, both before any release:

- **`Operation` is a function port, and `internal/core/CLAUDE.md` now says so.**
  The layer's "no `context` outside interface signatures" rule predates this
  domain and `Operation` was its first exception. Rather than leave code and
  rule divergent, the rule is amended to admit a *single-method function port*
  — a named `func(ctx) error` that IS the contract, the function-shaped
  equivalent of a one-method interface (cf. `http.HandlerFunc`). The exception
  is deliberately narrow: it does not admit `context` in struct fields, value
  types, or package-level state.
- **`Timeout` treats the deadline as authoritative.** `Run` previously
  consulted `dctx.Err()` only when the Operation returned a non-nil error, so
  an Operation that ignored `ctx` and reported success after the deadline made
  the policy report success — silently voiding it. The documented degradation
  for a ctx-ignoring Operation is reduced *precision* (failure detected at op
  granularity), not an abandoned deadline.

## Deferred

- ~~Retry jitter.~~ **Landed** as `RetryConfig.Jitter`, on the same terms as the
  retryable-error classifier below: an opt-in parameter on an existing policy,
  not a new policy, whose ZERO value preserves the behaviour decided above. A
  `float64` fraction of the computed delay, drawn uniformly from `[0, Jitter)`
  and ADDED to it, clamped into `[0, 1]`.

  Three things the implementation decides, which the deferral did not:

  - **Additive, not proportional.** `delay + U[0, delay*J)` keeps the average
    wait growing monotonically with the attempt. Full jitter — `U[0, delay)` —
    spreads harder but lets a late attempt wait less than an early one, which
    turns a backoff into a lottery.
  - **Applied AFTER the `MaxDelay` cap.** The cap bounds the growth; jitter
    spreads callers *around* the bound rather than being squeezed flat against
    it. The consequence is stated in the field's own doc: *when a cap is
    configured*, the effective ceiling on one wait becomes
    `MaxDelay * (1 + Jitter)`. A zero `MaxDelay` is no cap, so there is no
    ceiling to raise — the backoff grows geometrically and the jitter widens
    whatever it reaches, bounded only by what a `time.Duration` represents.
  - **`backoff` stays a pure function of the attempt number**, and jitter is
    applied in `wait`. The deterministic growth and the randomisation are two
    different claims, and a suite that cannot assert the first without the
    second can assert neither precisely.

  **This is a published-shape change, and ADR 0040 is why it is allowed.**
  `pkg/v1/resilience.RetryConfig` is an alias onto the service type, so its
  arity changed: `{MaxAttempts, BaseDelay, MaxDelay, Multiplier, Retryable}`
  became `{…, Jitter}`. Any downstream UNKEYED composite literal stops
  compiling. That is permitted only because `pkg` is still v0 and Go promises
  nothing across v0 minors — a licence that expires at `pkg/v1.0.0`, after
  which the same edit would need a sibling type or a `pkg/v2` path.

  The trigger was a concrete consumer: `kodflow/ktn-linter` carried its own
  jittered dial backoff for the proxy→daemon socket handshake, with the
  thundering-herd reason written beside it, because this policy could not
  express it.

- (The retryable-error classifier that shipped alongside jitter in this list has
  since landed as `RetryConfig.Retryable` / `BreakerConfig.Retryable`
  — a `func(error) bool` whose `nil` value preserves the behaviour decided above.
  The Decision section is unchanged: the classifier is an opt-in parameter on the
  two existing policies, not a new policy.)
- Wait-mode (blocking) bulkhead/rate-limiter (v1 is reject-mode).
- Half-open concurrency control (v1 admits HalfOpen trials without a probe cap).
- **Adaptive concurrency (AIMD) and deadline propagation.** Both were examined
  when `fallback`/`hedging` landed (below) and left out on purpose: each changes
  what a policy may decide on the caller's behalf, and each needs measurement
  rather than a plausible implementation. `MaxInFlight` is a *static* cap
  deliberately — an adaptive one that shrinks under load is the AIMD design, and
  choosing its constants without data is how a load-shedding mechanism becomes a
  second outage.

## Extension — `fallback` and `hedging` (Decision 2 now names seven policies)

Two compositions landed after the original five, in the same shape (concrete
`Runner`, no registry, same `0.2.8.*` block, no new range owner):

- **`NewFallback`** runs a secondary `Operation` when the primary fails. A
  successful fallback returns `nil` — that masking is the policy. When both
  halves fail the result is the new `FallbackFailed` sentinel (`0.2.8.7`,
  `EX_TEMPFAIL`) carrying BOTH messages as the `primary` and `fallback` fields:
  returning either error alone makes the outcome undiagnosable, and promoting
  either to the wrap origin would let an `*errs.Error` half hijack the policy
  code (the rule `wrapAs` exists to enforce). A nil `Fallback` is refused
  (ADR 0031).
- **`NewHedge`** duplicates an `Operation` still outstanding after `Delay` and
  takes the first success. It is the only policy that runs the operation
  concurrently with itself, so it carries two obligations the others do not, and
  ADR 0031's rule is applied to both: `Idempotent` is a **mandatory in-code
  assertion** (the SDK cannot detect idempotence, and the symptom of getting it
  wrong is a silent double effect, so a doc comment is the wrong instrument),
  and `Delay` + `MaxInFlight` are **refused** rather than defaulted — a zero
  delay duplicates every call, and both directions of a guessed cap are harmful
  (too low is inert, too high is an amplifier). `MaxHedges` clamps to 1, which
  is the contrast that locates the clamp/refuse line. Hedging fires on latency
  only; reacting to a failure is `NewRetry`'s job, and reaching `MaxInFlight`
  degrades the policy to no-hedging rather than to a rejection.

Rationale in full: `internal/service/resilience/CLAUDE.md` §Fallback / §Hedging.

## References

- Impl: `internal/core/resilience/`, `internal/service/resilience/`, `pkg/v1/resilience/`.
- ADR 0016 (no-registry sibling), ADR 0024 (Phase-B wave), ADR 0018 (portability).
