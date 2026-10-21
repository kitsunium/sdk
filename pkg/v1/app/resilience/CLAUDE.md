# pkg/v1/app/resilience/

## Purpose

Public facade for the SDK reliability policies (ADR 0026). Aliases the
`Operation`/`Runner` port + `*Config` types and `Backoff`, re-exports the eight
constructors (`NewRetry`/`NewCircuitBreaker`/`NewRateLimiter`/
`NewKeyedRateLimiter`/`NewBulkhead`/`NewTimeout`/`NewFallback`/`NewHedge`),
`NewTimeoutWith`, `RetryAfter`, `NewRetryBudget`, the breaker's state (`BreakerState`,
`BreakerStateValue`, `BreakerPhase` and its three constants, `BreakerWindowValue`)
and the outcome sentinels. Stdlib-only → dep-light;
cross-OS portable.

## Surface

| Symbol | Notes |
|---|---|
| `Operation` / `Runner` | aliases onto `core/app/resilience` |
| `RetryConfig` / `BreakerConfig` / `RateLimiterConfig` / `FallbackConfig` / `HedgeConfig` | aliases onto `service/app/resilience` |
| `RetryConfig.Retryable` / `BreakerConfig.Retryable` / `FallbackConfig.Retryable` | `func(error) bool` — deterministic errors return verbatim (nil = replay/count/fall-back on everything) |
| `RetryConfig.Jitter` | `float64` — widens each backoff by `[0, Jitter)` of itself, ADDED, clamped into `[0, 1]`. **0 is the deterministic backoff**, so an existing caller's timing is unchanged. Applied after the `MaxDelay` cap, so **when one is configured** a wait's ceiling is `MaxDelay * (1 + Jitter)`; a zero `MaxDelay` is no cap, so there is none to raise (ADR 0026) |
| `BreakerConfig.Window` / `BreakerWindowValue` | `*BreakerWindowValue{Size, Buckets, MinCalls, FailureRatio, SlowAfter, SlowRatio}` — nil is the consecutive breaker, unchanged; set, the breaker opens on a rate of failures or of slow calls over a sliding time window on its clock once `MinCalls` were seen. Zero fields take Polly's / resilience4j's defaults (30 s, 10 slots, 100 calls, 0.1; 60 s, 1); out of range → `PolicyMisconfigured` naming the field (#480) |
| `BreakerState` / `BreakerStateValue` / `BreakerPhase` + `BreakerClosed` / `BreakerOpen` / `BreakerHalfOpen` | aliases onto `core/app/resilience`: the breaker's Runner is a `BreakerState` (`State`, `Isolate` — open until `Reset` —, `Reset`), reached by a type assertion; a refused breaker is not one. The zero `BreakerPhase` is "unknown" (#480) |
| `BreakerConfig.OnStateChange` / `BreakerConfig.OpenBackoff` | `func(from, to BreakerPhase)` called outside the lock, one at a time, in order; `*Backoff` lengthening each opening while trials fail, `BaseDelay` and `MaxDelay` required (#480) |
| `HedgeConfig.Idempotent` | **mandatory** `bool` — the caller's in-code assertion that duplicating the operation is safe; false refuses the policy |
| `RetryConfig.Clock` | `clock.Timed` the backoff waits on — nil is the wall clock; a `ManualClock` from `pkg/v1/clock` drives a retry without sleeping (ADR 0103) |
| `HedgeConfig.Clock` | `clock.Timed` the hedge delay is measured on — nil is the wall clock; a `ManualClock` issues the duplicate when it is advanced past `Delay` |
| `RetryBudget` / `RetryBudgetConfig` / `NewRetryBudget` | the sibling port (alias onto `core/app/resilience`) and gRPC A6's token bucket: full at `MaxTokens`, a failure −1, a success +`TokenRatio`, extra attempts while more than half remain; `MaxTokens ≤ 0` or a `TokenRatio` below 0.001 refused → `PolicyMisconfigured` from the policies given it (#475) |
| `RetryConfig.Budget` / `HedgeConfig.Budget` / `RetryConfig.OnRetry` | one budget per dependency, shared by its retry (refusal → `RetryBudgetExceeded`) and its hedge (refusal → no duplicate); nil = no budget. `OnRetry(attempt, err, delay)` — synchronous, before each wait, for the caller's own metrics (#475) |
| `RetryConfig.Delay` / `RetryAfter` | `func(attempt, err) (time.Duration, bool)` — the failure imposes the wait (held at `MaxDelay`, no jitter); nil = the curve. `RetryAfter(err)` has that shape and reads `retry_after_seconds`, set by the OTLP exporters, `net/client` on 429/503 and `RateLimited` (#476) |
| `TimeoutConfig` / `NewTimeoutWith` | the deadline (`Duration`, refused when not positive) and the `clock.Timed` it is measured on; nil or `clock.System` is `NewTimeout` exactly, a `ManualClock` expires it when advanced (ADR 0090, #477) |
| `KeyedRateLimiterConfig` | alias onto `service/app/resilience` — `Rate`/`Burst` per key, `Key func(ctx) string`, `MaxKeys`, `IdleTimeout`, `Clock`; `Rate`/`Key`/`MaxKeys`/`IdleTimeout` refused at zero (ADR 0103) |
| `Backoff` | alias onto `kernel/backoff.Value`, the layer that owns the curve (ADR 0074) — `BaseDelay`/`MaxDelay`/`Multiplier`/`Jitter` + `Delay(attempt)`, the curve `NewRetry` waits, never negative (ADR 0103) |
| `NewRetry` / `NewCircuitBreaker` / `NewRateLimiter` / `NewKeyedRateLimiter` / `NewBulkhead` / `NewTimeout` / `NewFallback` / `NewHedge` | constructors |
| `RetryExhausted` / `RetryBudgetExceeded` / `CircuitOpen` / `RateLimited` / `BulkheadFull` / `TimeoutExceeded` / `FallbackFailed` | sentinels (`errs.HasReason`/`HasCode`), all `EX_TEMPFAIL`; the four REJECTIONS carry the HTTP status a framework answers with through `errs.HTTPStatusOf` — `RateLimited` 429, `CircuitOpen` and `BulkheadFull` 503, `TimeoutExceeded` 504 — and `RetryExhausted` / `FallbackFailed` carry none (500), since what failed there is the operation; `FallbackFailed` carries the `primary` + `fallback` messages as fields |
| `PolicyMisconfigured` | sentinel for a policy that cannot honour its config — **permanent**, `EX_CONFIG`, never retry (ADR 0031) |

## Conventions

- **Type aliases, not new types**; constructors are thin delegations.
- **Compose by nesting**: `Retry(Breaker(Timeout(op)))` via Runner-in-Operation.
- **No inert policies** (ADR 0031): a non-positive knob is clamped to a working
  floor where one is obvious (`BreakerConfig.OpenDuration`→30s) and refused
  where it is not. A policy never silently declines to do its job.
- **A caller who leaves is not a failing dependency** (#477): a
  `context.Canceled` under a closed caller context is neutral to the breaker,
  and the timeout returns the caller's own context error, not
  `TimeoutExceeded`, when that context finished first.
- **One classifier for three policies**: `Retryable` has the same shape in
  `RetryConfig`, `BreakerConfig` and `FallbackConfig`, so a nested stack shares
  a single predicate.
- **Hedging is opt-in per operation, in code.** `HedgeConfig.Idempotent` is a
  mandatory assertion, not a hint: the SDK cannot detect whether duplicating an
  operation double-charges a card, and the symptom of getting it wrong is a
  silent success, not an error. The zero value refuses the policy, so hedging is
  never reached by copying a config and dropping a field. See
  `internal/service/app/resilience/CLAUDE.md` §Hedging for the full argument,
  including the `MaxInFlight` load cap and why reaching it degrades to
  no-hedging rather than to a rejection.
- README written by `tools/genindex` from `docs/api` (ADR 0167); the package
  comment is in `doc.go`, which kit writes from `design/app/resilience.yaml`: edit the
  design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Reimplement policy logic here — the facade is aliases + constructor wrappers.

## Generated

`gen_facade.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/resilience.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `gen_facade.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `gen_facade.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/app/resilience:resilience_test
```
